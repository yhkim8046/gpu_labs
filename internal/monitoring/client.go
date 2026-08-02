package monitoring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/gpu-lab/gpu-lab/internal/runner"
)

const (
	KubeContext   = "gpu-lab"
	Namespace     = "gpu-lab-monitoring"
	PrometheusSvc = "gpu-lab-monitoring-kube-pr-prometheus:9090"
)

// Client queries Prometheus through the Kubernetes API proxy, so users do not
// need to manage a separate port-forward for every metrics command.
type Client struct {
	Runner    runner.Runner
	Context   string
	Namespace string
	Service   string
}

func New(r runner.Runner) Client {
	return Client{Runner: r, Context: KubeContext, Namespace: Namespace, Service: PrometheusSvc}
}

type Sample struct {
	Metric map[string]string `json:"metric,omitempty"`
	Value  float64           `json:"value"`
}

type QueryResult struct {
	ResultType string   `json:"result_type"`
	Samples    []Sample `json:"samples"`
}

type response struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

type vectorSample struct {
	Metric map[string]string `json:"metric"`
	Value  []json.RawMessage `json:"value"`
}

func (c Client) Query(ctx context.Context, expression string) (QueryResult, error) {
	if expression == "" {
		return QueryResult{}, errors.New("PromQL expression cannot be empty")
	}
	proxyPath := "/api/v1/namespaces/" + c.Namespace + "/services/" + c.Service + "/proxy/api/v1/query?query=" + url.QueryEscape(expression)
	data, err := c.Runner.Output(ctx, "kubectl", "--context", c.Context, "get", "--raw", proxyPath)
	if err != nil {
		return QueryResult{}, fmt.Errorf("query Prometheus: %w", err)
	}
	return decode([]byte(data))
}

func decode(data []byte) (QueryResult, error) {
	var parsed response
	if err := json.Unmarshal(data, &parsed); err != nil {
		return QueryResult{}, fmt.Errorf("decode Prometheus response: %w", err)
	}
	if parsed.Status != "success" {
		if parsed.Error != "" {
			return QueryResult{}, fmt.Errorf("Prometheus response status is %q: %s", parsed.Status, parsed.Error)
		}
		return QueryResult{}, fmt.Errorf("Prometheus response status is %q", parsed.Status)
	}
	result := QueryResult{ResultType: parsed.Data.ResultType}
	switch parsed.Data.ResultType {
	case "vector":
		var values []vectorSample
		if err := json.Unmarshal(parsed.Data.Result, &values); err != nil {
			return QueryResult{}, fmt.Errorf("decode Prometheus vector: %w", err)
		}
		for _, item := range values {
			value, err := decodeValue(item.Value)
			if err != nil {
				return QueryResult{}, err
			}
			result.Samples = append(result.Samples, Sample{Metric: item.Metric, Value: value})
		}
	case "scalar":
		var value []json.RawMessage
		if err := json.Unmarshal(parsed.Data.Result, &value); err != nil {
			return QueryResult{}, fmt.Errorf("decode Prometheus scalar: %w", err)
		}
		decoded, err := decodeValue(value)
		if err != nil {
			return QueryResult{}, err
		}
		result.Samples = []Sample{{Value: decoded}}
	default:
		return QueryResult{}, fmt.Errorf("unsupported Prometheus result type %q", parsed.Data.ResultType)
	}
	if len(result.Samples) == 0 {
		return QueryResult{}, errors.New("Prometheus query returned no result")
	}
	return result, nil
}

func decodeValue(value []json.RawMessage) (float64, error) {
	if len(value) < 2 {
		return 0, errors.New("Prometheus result does not contain a value")
	}
	var raw string
	if err := json.Unmarshal(value[1], &raw); err != nil {
		return 0, fmt.Errorf("decode Prometheus value: %w", err)
	}
	decoded, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("parse Prometheus value %q: %w", raw, err)
	}
	return decoded, nil
}
