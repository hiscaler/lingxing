package oauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hiscaler/lingxing"
)

type AuthResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func (s service) Auth(appId, appSecret string) (ar AuthResponse, err error) {
	result := struct {
		lingxing.NormalResponse
		Data AuthResponse `json:"data"`
	}{}
	resp, err := s.lingXing.Client.R().
		SetResult(&result).
		Post(fmt.Sprintf("/api/auth-server/oauth/access-token?appId=%s&appSecret=%s", appId, appSecret))
	if err != nil {
		return
	}

	if resp.IsSuccess() {
		err = lingxing.ErrorWrap(result.Code, result.Message)
	} else {
		if e := json.Unmarshal(resp.Body(), &result); e == nil {
			err = lingxing.ErrorWrap(result.Code, result.Message)
		} else {
			err = errors.New(resp.Status())
		}
	}
	return
}

func (s service) Refresh() (ar AuthResponse, err error) {
	return
}