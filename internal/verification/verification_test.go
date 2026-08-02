package verification

import "testing"

func TestDecodePrometheusValue(t *testing.T) {
	value, err := decodePrometheusValue([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"value":["1","95"]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if value != 95 {
		t.Fatalf("value = %v, want 95", value)
	}
}

func TestDecodePrometheusValueRejectsEmptyResult(t *testing.T) {
	if _, err := decodePrometheusValue([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`)); err == nil {
		t.Fatal("expected empty result error")
	}
}
