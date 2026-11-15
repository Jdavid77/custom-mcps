package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	k8sAPI = "https://kubernetes.default.svc"
	saDir  = "/var/run/secrets/kubernetes.io/serviceaccount"
)

// bearer adds the ServiceAccount token, re-read per request since the kubelet rotates it.
type bearer struct{ next http.RoundTripper }

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := os.ReadFile(saDir + "/token")
	if err != nil {
		return nil, err
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+string(token))
	return b.next.RoundTrip(req)
}

var k8sClient = sync.OnceValues(func() (*http.Client, error) {
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("no certificates in " + saDir + "/ca.crt")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	return &http.Client{Timeout: 30 * time.Second, Transport: bearer{transport}}, nil
})

func k8sList(ctx context.Context, version, resource string, out any) error {
	c, err := k8sClient()
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/apis/external-secrets.io/%s/%s", k8sAPI, version, resource)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	return send(c, req, out)
}

func normalizeKey(key string) string {
	return "/" + strings.TrimLeft(key, "/")
}

// usedKeys returns the Akeyless keys referenced by ExternalSecrets and PushSecrets.
func usedKeys(ctx context.Context) (map[string]bool, error) {
	used := map[string]bool{}
	var externalSecrets struct {
		Items []struct {
			Spec struct {
				Data []struct {
					RemoteRef struct {
						Key string `json:"key"`
					} `json:"remoteRef"`
				} `json:"data"`
				DataFrom []struct {
					Extract *struct {
						Key string `json:"key"`
					} `json:"extract"`
				} `json:"dataFrom"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := k8sList(ctx, "v1", "externalsecrets", &externalSecrets); err != nil {
		return nil, err
	}
	for _, es := range externalSecrets.Items {
		for _, d := range es.Spec.Data {
			used[normalizeKey(d.RemoteRef.Key)] = true
		}
		for _, d := range es.Spec.DataFrom {
			if d.Extract != nil {
				used[normalizeKey(d.Extract.Key)] = true
			}
		}
	}
	var pushSecrets struct {
		Items []struct {
			Spec struct {
				Data []struct {
					Match struct {
						RemoteRef struct {
							RemoteKey string `json:"remoteKey"`
						} `json:"remoteRef"`
					} `json:"match"`
				} `json:"data"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := k8sList(ctx, "v1alpha1", "pushsecrets", &pushSecrets); err != nil {
		return nil, err
	}
	for _, ps := range pushSecrets.Items {
		for _, d := range ps.Spec.Data {
			used[normalizeKey(d.Match.RemoteRef.RemoteKey)] = true
		}
	}
	return used, nil
}
