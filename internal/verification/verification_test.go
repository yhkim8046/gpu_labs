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

func TestFabricVerificationQueriesUseExactNodePortLabels(t *testing.T) {
	want := `max(gpu_lab_ib_port_up{node="gpu-lab-worker2",hca="mlx5_0",port="1",link_layer="InfiniBand"})`
	if got := fabricMetric("gpu_lab_ib_port_up", "gpu-lab-worker2"); got != want {
		t.Fatalf("fabric metric query = %q, want %q", got, want)
	}
	want = `max(gpu_lab_ib_port_state{node="gpu-lab-worker2",hca="mlx5_0",port="1",link_layer="InfiniBand",state="DOWN",physical_state="DISABLED"})`
	if got := fabricStateMetric("gpu-lab-worker2", "DOWN", "DISABLED"); got != want {
		t.Fatalf("fabric state query = %q, want %q", got, want)
	}
}
