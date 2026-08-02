package monitoring

import "testing"

func TestDecodeVector(t *testing.T) {
	result, err := decode([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"node":"gpu-node-01"},"value":["1","95.5"]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.ResultType != "vector" || len(result.Samples) != 1 {
		t.Fatalf("result = %#v, want one vector sample", result)
	}
	if result.Samples[0].Metric["node"] != "gpu-node-01" || result.Samples[0].Value != 95.5 {
		t.Fatalf("sample = %#v, want node and value", result.Samples[0])
	}
}

func TestDecodeScalar(t *testing.T) {
	result, err := decode([]byte(`{"status":"success","data":{"resultType":"scalar","result":["1","3"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Samples) != 1 || result.Samples[0].Value != 3 {
		t.Fatalf("samples = %#v, want scalar 3", result.Samples)
	}
}

func TestDecodeRejectsEmptyResult(t *testing.T) {
	if _, err := decode([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`)); err == nil {
		t.Fatal("decode() succeeded for empty result")
	}
}
