//go:build componenttest

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"

	"github.com/euyuil/fleeting-plugin-aliyun/internal/aliyun"
)

// This factory is compiled only into the component-test binary.
func newAPI(region string) (aliyun.API, error) {
	endpoint := os.Getenv("ALIYUN_COMPONENT_ENDPOINT")
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
		return nil, errors.New("component endpoint must use numeric loopback HTTP")
	}
	return &componentAPI{endpoint: endpoint}, nil
}

type componentAPI struct{ endpoint string }

func (c *componentAPI) Call(ctx context.Context, service, action string, q map[string]string, out any) error {
	b, err := json.Marshal(struct {
		Service, Action string
		Query           map[string]string
	}{service, action, q})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("component API failed")
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
