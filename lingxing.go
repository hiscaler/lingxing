package lingxing

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/go-resty/resty/v2"
	"github.com/hiscaler/gox/bytex"
	"github.com/hiscaler/gox/cryptox"
	"github.com/hiscaler/gox/jsonx"
	"github.com/hiscaler/gox/stringx"
	"github.com/hiscaler/lingxing/config"
	jsoniter "github.com/json-iterator/go"
	"github.com/json-iterator/go/extra"
	"github.com/spf13/cast"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"
)

func init() {
	extra.RegisterFuzzyDecoders()
}

// https://openapidoc.lingxing.com/#/docs/Guidance/ErrorCode
const (
	OK                       = 200     // 无错误
	ServiceNotFoundError     = 400     // 服务不存在
	InternalError            = 500     // 内部错误，数据库异常
	AppIdNotExistError       = 2001001 // appId 不存在
	InvalidAppSecretError    = 2001002 // appSecret 不正确或者 urlencode 需要进行编码
	AccessTokenExpireError   = 2001003 // token 不存在或者已经过期
	UnauthorizedError        = 2001004 // api 未授权
	InvalidAccessTokenError  = 2001005 // token 不正确
	SignError                = 2001006 // 签名错误
	SignExpiredError         = 2001007 // 签名过期
	RefreshTokenExpiredError = 2001008 // RefreshToken 过期
	InvalidRefreshTokenError = 2001009 // 无效的 RefreshToken
	InvalidQueryParamsError  = 3001001 // 查询参数缺失
	InvalidIPError           = 3001002 // 应用所在服务器的 ip 不在白名单中
	TooManyRequestsError     = 3001008 // 接口请求超请求次数限额
)

const (
	Version   = "0.0.1"
	userAgent = "LingXing API Client-Golang/" + Version + " (https://github.com/hiscaler/lingxing)"
)

var ErrNotFound = errors.New("lingxing: not found")

type LingXing struct {
	sandbox   bool
	appId     string
	appSecret string
	Debug     bool // 是否调试模式
	auth      AuthResponse
	Services  services // API Services
}

func NewLingXing(config config.Config) *LingXing {
	logger := log.New(os.Stdout, "[ LingXing ] ", log.LstdFlags|log.Llongfile)
	var appId, appSecret string
	if config.Sandbox {
		appId = config.Environment.Dev.AppId
		appSecret = config.Environment.Dev.AppSecret
	} else {
		appId = config.Environment.Prod.AppId
		appSecret = config.Environment.Prod.AppSecret
	}
	lingXingClient := &LingXing{
		sandbox:   config.Sandbox,
		appId:     appId,
		appSecret: appSecret,
		Debug:     config.Debug,
	}
	httpClient := resty.New().SetDebug(config.Debug).
		SetHeaders(map[string]string{
			"Content-Type": "application/json",
			"Accept":       "application/json",
			"User-Agent":   userAgent,
		})
	if config.Sandbox {
		httpClient.SetBaseURL("https://openapisandbox.lingxing.com/erp/sc")
	} else {
		httpClient.SetBaseURL("https://openapi.lingxing.com/erp/sc")
	}

	httpClient.SetTimeout(10 * time.Second).
		OnBeforeRequest(func(client *resty.Client, request *resty.Request) error {
			if err := lingXingClient.accessToken(); err != nil {
				logger.Printf("auth error: %s", err.Error())
				return err
			}

			client.SetAuthToken(lingXingClient.auth.AccessToken)

			appendQueryParams := map[string]string{
				"app_key":      lingXingClient.appId,
				"access_token": lingXingClient.auth.AccessToken,
				"timestamp":    strconv.FormatInt(time.Now().Unix(), 10),
			}
			params := make(map[string]interface{}, 0)
			for k, v := range appendQueryParams {
				params[k] = v
			}
			// 获取 URL 请求参数
			if u, err := url.Parse(request.URL); err == nil && len(u.Query()) > 0 {
				for k := range u.Query() {
					params[k] = u.Query().Get(k)
				}
			}
			for k := range request.QueryParam {
				params[k] = request.QueryParam.Get(k)
			}

			if request.Method == http.MethodPost {
				bodyParams := cast.ToStringMap(jsonx.ToJson(request.Body, "{}")) // Body
				for k, v := range bodyParams {
					params[k] = v
				}
			}
			sign, err := generateSign(lingXingClient.appId, params)
			if err != nil {
				return err
			}

			appendQueryParams["sign"] = url.QueryEscape(sign)
			request.SetQueryParams(appendQueryParams)
			return nil
		}).
		OnAfterResponse(func(client *resty.Client, response *resty.Response) (err error) {
			if response.IsError() {
				return fmt.Errorf("%s: %s", response.Status(), bytex.ToString(response.Body()))
			}

			r := struct {
				Code         interface{} `json:"code"`
				Message      string      `json:"message"`
				Msg          string      `json:"msg"`
				ErrorDetails interface{} `json:"error_details"` // 存在多种返回格式：string, string slice, struct slice
			}{}
			if err = jsoniter.Unmarshal(response.Body(), &r); err == nil {
				if r.Code != 0 {
					if r.ErrorDetails != nil {
						if s, ok := r.ErrorDetails.(string); ok {
							err = ErrorWrap(cast.ToInt(r.Code), s)
						} else if ss, ok := r.ErrorDetails.([]interface{}); ok {
							type errorDetail struct {
								Message string `json:"message"`
							}
							removeString := "错误："
							n := len(removeString)
							errorMessages := make([]string, 0)
							for i := range ss {
								message := ""
								if s, ok := ss[i].(string); ok {
									message = s
								} else if ed, ok := ss[i].(errorDetail); ok {
									message = ed.Message
								}
								message = strings.TrimSpace(message)
								if message != "" {
									if index := strings.Index(message, removeString); index == 0 {
										message = message[n:]
									}
									if index := strings.Index(message, " => "); index != -1 {
										message = message[index+4:]
									}
									errorMessages = append(errorMessages, message)
								}
							}
							err = ErrorWrap(cast.ToInt(r.Code), strings.Join(errorMessages, "；"))
						}
					} else {
						msg := r.Message
						if msg == "" {
							msg = r.Msg
						}
						err = ErrorWrap(cast.ToInt(r.Code), msg)
					}
				}
			} else {
				logger.Printf("JSON Unmarshal error: %s", err.Error())
			}

			if err != nil {
				logger.Printf("OnAfterResponse error: %s", err.Error())
			}
			return
		}).
		SetRetryCount(2).
		SetRetryWaitTime(5 * time.Second).
		SetRetryMaxWaitTime(10 * time.Second).
		AddRetryCondition(func(response *resty.Response, err error) bool {
			if response == nil {
				return false
			}

			retry := response.StatusCode() == http.StatusTooManyRequests
			if !retry {
				r := struct{ Code int }{}
				retry = jsoniter.Unmarshal(response.Body(), &r) == nil && r.Code == TooManyRequestsError
			}
			if retry {
				text := response.Request.URL
				if err != nil {
					text += fmt.Sprintf(", error: %s", err.Error())
				}
				logger.Printf("Retry request: %s", text)
			}
			return retry
		})

	jsoniter.RegisterTypeDecoderFunc("float64", func(ptr unsafe.Pointer, iter *jsoniter.Iterator) {
		switch iter.WhatIsNext() {
		case jsoniter.StringValue:
			var t float64
			v := strings.TrimSpace(iter.ReadString())
			if v != "" {
				var err error
				if t, err = strconv.ParseFloat(v, 64); err != nil {
					iter.Error = err
					return
				}
			}
			*((*float64)(ptr)) = t
		default:
			*((*float64)(ptr)) = iter.ReadFloat64()
		}
	})
	jsoniter.RegisterTypeDecoderFunc("bool", func(ptr unsafe.Pointer, iter *jsoniter.Iterator) {
		switch iter.WhatIsNext() {
		case jsoniter.StringValue:
			var t bool
			v := strings.TrimSpace(iter.ReadString())
			if v != "" {
				var err error
				if t, err = strconv.ParseBool(strings.ToLower(v)); err != nil {
					iter.Error = err
					return
				}
			}
			*((*bool)(ptr)) = t
		case jsoniter.NumberValue:
			if v, err := iter.ReadNumber().Int64(); err != nil {
				iter.Error = err
				return
			} else {
				*((*bool)(ptr)) = v > 0
			}
		default:
			*((*bool)(ptr)) = iter.ReadBool()
		}
	})
	httpClient.JSONMarshal = jsoniter.Marshal
	httpClient.JSONUnmarshal = jsoniter.Unmarshal
	xService := service{
		debug:      config.Debug,
		logger:     logger,
		httpClient: httpClient,
	}
	lingXingClient.Services = services{
		BasicData: (basicDataService)(xService),
		CustomerService: customerServiceService{
			Email:  (customerServiceEmailService)(xService),
			Review: (customerServiceReviewService)(xService),
		},
		Product: productService{
			productProductService: (productProductService)(xService),
			Brand:                 (productBrandService)(xService),
			Category:              (productCategoryService)(xService),
			AuxMaterial:           (productAuxMaterialService)(xService),
			Bundle:                (productBundledService)(xService),
		},
		Sale: saleService{
			FBM: saleFBMService{
				Order: (fbmOrderService)(xService),
			},
			Order:   (orderService)(xService),
			Listing: (listingService)(xService),
			Review:  (reviewService)(xService),
		},
		FBA: fbaService{
			Shipment:   (fbaShipmentService)(xService),
			StorageFee: (fbaStorageFeeService)(xService),
		},
	}
	return lingXingClient
}

type NormalResponse struct {
	Total int `json:"total"`
}

type AuthResponse struct {
	AccessToken     string    `json:"access_token"`
	RefreshToken    string    `json:"refresh_token"`
	ExpiresIn       int       `json:"expires_in"`
	ExpiresDatetime time.Time `json:"-"`
}

func (lx *LingXing) accessToken() (err error) {
	auth := lx.auth
	if auth.AccessToken != "" && auth.ExpiresDatetime.After(time.Now()) {
		return nil
	}

	result := struct {
		Code    string       `json:"code"`
		Message string       `json:"msg"`
		Data    AuthResponse `json:"data"`
	}{}
	httpClient := resty.New().
		SetDebug(lx.Debug).
		SetHeaders(map[string]string{
			"Content-Type": "application/json",
			"Accept":       "application/json",
			"User-Agent":   userAgent,
		})
	if lx.sandbox {
		httpClient.SetBaseURL("https://openapisandbox.lingxing.com")
	} else {
		httpClient.SetBaseURL("https://openapi.lingxing.com")
	}
	url := fmt.Sprintf("/api/auth-server/oauth/access-token?appId=%s&appSecret=%s", lx.appId, url.QueryEscape(lx.appSecret))
	if auth.RefreshToken != "" {
		url = fmt.Sprintf("/api/auth-server/oauth/refresh?appId=%s&refreshToken=%s", lx.appId, auth.RefreshToken)
	}
	resp, err := httpClient.R().
		SetResult(&result).
		Post(url)
	if err != nil {
		return
	}

	if resp.IsSuccess() {
		code, _ := strconv.ParseInt(result.Code, 10, 32)
		err = ErrorWrap(int(code), result.Message)
		if err == nil {
			ar := result.Data
			ar.ExpiresDatetime = time.Now().Add(time.Duration(ar.ExpiresIn*8/10) * time.Second) // 剩余 1/5 时间就会要求更换 token
			lx.auth = ar
		}
	} else {
		err = fmt.Errorf("%s: %s", resp.Status(), bytex.ToString(resp.Body()))
	}
	return
}

func generateSign(appId string, params map[string]interface{}) (sign string, err error) {
	n := len(params)
	keys := make([]string, n)
	i := 0
	for k := range params {
		keys[i] = k
		i++
	}
	sort.Strings(keys)

	sb := strings.Builder{}
	for _, key := range keys {
		sb.WriteString(key)
		sb.WriteRune('=')
		switch v := params[key].(type) {
		case string:
			sb.WriteString(v)
		default:
			var b []byte
			b, err = jsoniter.Marshal(v)
			if err == nil {
				sb.Write(b)
			} else {
				return
			}
		}
		sb.WriteRune('&')
	}
	s := sb.String()
	if n = len(s); n > 0 {
		s = s[0 : n-1]
	}

	aesTool := NewAesTool(stringx.ToBytes(appId), len(appId))
	aesEncrypted, err := aesTool.ECBEncrypt(stringx.ToBytes(strings.ToUpper(cryptox.Md5(s))))
	if err != nil {
		return
	}

	sign = base64.StdEncoding.EncodeToString(aesEncrypted)
	return
}

// ErrorWrap 错误包装
func ErrorWrap(code int, message string) error {
	if code == OK || code == 0 {
		return nil
	}

	switch code {
	case ServiceNotFoundError:
		message = "服务不存在"
	case AppIdNotExistError:
		message = "appId 不存在"
	case InvalidAppSecretError:
		message = "appSecret 不正确或者未编码"
	case AccessTokenExpireError:
		message = "Token 不存在或者已经过期"
	case UnauthorizedError:
		message = "API 未授权"
	case InvalidAccessTokenError:
		message = "token 不正确"
	case SignError:
		message = "签名错误"
	case SignExpiredError:
		message = "签名过期"
	case RefreshTokenExpiredError:
		message = "Refresh Token 过期"
	case InvalidRefreshTokenError:
		message = "无效的 Refresh Token"
	case InvalidQueryParamsError:
		message = "查询参数缺失"
	case InvalidIPError:
		message = "应用所在服务器的 IP 不在白名单中"
	case TooManyRequestsError:
		message = "接口请求超请求次数限额"
	default:
		if code == InternalError {
			if message == "" {
				message = "内部错误"
			}
		} else {
			message = strings.TrimSpace(message)
		}
	}
	return fmt.Errorf("%d: %s", code, message)
}
