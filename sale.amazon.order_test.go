package lingxing

import (
	"github.com/hiscaler/gox/jsonx"
	"testing"
)

func TestOrderService_All(t *testing.T) {
	params := AmazonOrdersQueryParams{
		StartDate: "2022-01-01 00:00:00",
		EndDate:   "2022-11-01 23:59:59",
		SID:       168,
	}
	items, _, _, err := lingXingClient.Services.Sale.Order.All(params)
	if err != nil {
		t.Errorf("Services.Sale.Order.All() error: %s", err.Error())
	} else {
		t.Log(jsonx.ToPrettyJson(items))
	}
}

func TestOrderService_One(t *testing.T) {
	detail, err := lingXingClient.Services.Sale.Order.One("113")
	if err != nil {
		t.Errorf("Services.Sale.Order.One() error: %s", err.Error())
	} else {
		t.Log(jsonx.ToPrettyJson(detail))
	}
}
