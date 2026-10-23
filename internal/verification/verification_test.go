package verification

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const testPodCreation = "2026-10-02T12:00:00Z"

func testPodJSON(uid, created string) string {
	return fmt.Sprintf(`{"kind":"Pod","metadata":{"name":"gpu-lab-scheduling-failure","namespace":"%s","uid":%q,"creationTimestamp":%q}}`, DemoNamespace, uid, created)
}

func testEventList(uid, kind, name, namespace, message, eventTime, created string) string {
	return fmt.Sprintf(`{"kind":"EventList","items":[{"kind":"Event","metadata":{"creationTimestamp":%q},"involvedObject":{"kind":%q,"name":%q,"namespace":%q,"uid":%q},"message":%q,"eventTime":%q}]}`, created, kind, name, namespace, uid, message, eventTime)
}

func testCoreEventList(uid, message, first, last, created string) string {
	return fmt.Sprintf(`{"kind":"EventList","items":[{"kind":"Event","metadata":{"creationTimestamp":%q},"involvedObject":{"kind":"Pod","name":"gpu-lab-scheduling-failure","namespace":%q,"uid":%q},"message":%q,"firstTimestamp":%q,"lastTimestamp":%q}]}`, created, DemoNamespace, uid, message, first, last)
}

func testPodEventOutput(pod func() string, events string, eventErr error) func(context.Context, string, ...string) (string, error) {
	return func(_ context.Context, command string, args ...string) (string, error) {
		if command != "kubectl" || len(args) < 3 || args[0] != "--context" || args[1] != KubeContext {
			return "", fmt.Errorf("unexpected command: %s %v", command, args)
		}
		switch args[2] {
		case "get":
			if len(args) > 3 && args[3] == "pod" {
				return pod(), nil
			}
			if len(args) > 3 && args[3] == "events" {
				return events, eventErr
			}
		}
		return "", fmt.Errorf("unexpected kubectl arguments: %v", args)
	}
}

func TestPodEventCheckRejectsSameNameEventFromOldUID(t *testing.T) {
	output := testPodEventOutput(
		func() string { return testPodJSON("current-uid", testPodCreation) },
		testEventList("old-uid", "Pod", "gpu-lab-scheduling-failure", DemoNamespace, "Insufficient nvidia.com/gpu", "2026-10-02T12:01:00Z", "2026-10-02T12:01:00Z"),
		nil,
	)
	matched, err := checkPodEventOnce(context.Background(), "gpu-lab-scheduling-failure", "Insufficient nvidia.com/gpu", func(ctx context.Context, command string, args ...string) (string, error) {
		if len(args) > 3 && args[2] == "get" && args[3] == "events" && !strings.Contains(strings.Join(args, " "), "involvedObject.uid=current-uid") {
			t.Fatalf("event query did not select current UID: %v", args)
		}
		return output(ctx, command, args...)
	})
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("event for the old same-name Pod UID was accepted")
	}
}

func TestPodEventCheckAcceptsCurrentUIDAndCompatibleCoreEventTime(t *testing.T) {
	podReads := 0
	output := testPodEventOutput(func() string {
		podReads++
		return testPodJSON("current-uid", testPodCreation)
	}, testCoreEventList("current-uid", "Insufficient nvidia.com/gpu", testPodCreation, "2026-10-02T12:00:01Z", "2026-10-02T12:00:01Z"), nil)
	matched, err := checkPodEventOnce(context.Background(), "gpu-lab-scheduling-failure", "Insufficient nvidia.com/gpu", output)
	if err != nil {
		t.Fatal(err)
	}
	if !matched || podReads != 2 {
		t.Fatalf("matched=%t, pod reads=%d; want a pass after confirming the UID twice", matched, podReads)
	}
}

func TestPodEventCheckVerifiesEventInvolvedObjectIdentity(t *testing.T) {
	for name, involved := range map[string]struct {
		kind      string
		podName   string
		namespace string
	}{
		"wrong kind":      {kind: "Node", podName: "gpu-lab-scheduling-failure", namespace: DemoNamespace},
		"wrong name":      {kind: "Pod", podName: "another-pod", namespace: DemoNamespace},
		"wrong namespace": {kind: "Pod", podName: "gpu-lab-scheduling-failure", namespace: "default"},
	} {
		t.Run(name, func(t *testing.T) {
			output := testPodEventOutput(
				func() string { return testPodJSON("current-uid", testPodCreation) },
				testEventList("current-uid", involved.kind, involved.podName, involved.namespace, "Insufficient nvidia.com/gpu", "2026-10-02T12:01:00Z", "2026-10-02T12:01:00Z"),
				nil,
			)
			matched, err := checkPodEventOnce(context.Background(), "gpu-lab-scheduling-failure", "Insufficient nvidia.com/gpu", output)
			if err != nil {
				t.Fatal(err)
			}
			if matched {
				t.Fatal("event for a different involvedObject was accepted")
			}
		})
	}
}

func TestPodEventCheckRejectsEventOlderThanCurrentPod(t *testing.T) {
	output := testPodEventOutput(
		func() string { return testPodJSON("current-uid", testPodCreation) },
		testEventList("current-uid", "Pod", "gpu-lab-scheduling-failure", DemoNamespace, "Insufficient nvidia.com/gpu", "2026-10-02T11:59:59Z", "2026-10-02T11:59:59Z"),
		nil,
	)
	matched, err := checkPodEventOnce(context.Background(), "gpu-lab-scheduling-failure", "Insufficient nvidia.com/gpu", output)
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("event timestamp before Pod creation was accepted")
	}
}

func TestPodEventCheckFailsIfPodIsRecreatedBeforePass(t *testing.T) {
	podReads := 0
	output := testPodEventOutput(func() string {
		podReads++
		if podReads == 1 {
			return testPodJSON("old-uid", testPodCreation)
		}
		return testPodJSON("new-uid", "2026-10-02T12:02:00Z")
	}, testEventList("old-uid", "Pod", "gpu-lab-scheduling-failure", DemoNamespace, "Insufficient nvidia.com/gpu", "2026-10-02T12:01:00Z", "2026-10-02T12:01:00Z"), nil)
	matched, err := checkPodEventOnce(context.Background(), "gpu-lab-scheduling-failure", "Insufficient nvidia.com/gpu", output)
	if err == nil || !strings.Contains(err.Error(), "recreated") {
		t.Fatalf("error = %v, want recreation race error", err)
	}
	if matched {
		t.Fatal("event passed across a Pod recreation race")
	}
}

func TestPodEventCheckFailsClosedForMissingUIDMalformedDataAndCommandErrors(t *testing.T) {
	t.Run("pod missing UID", func(t *testing.T) {
		output := testPodEventOutput(func() string { return testPodJSON("", testPodCreation) }, `{"kind":"EventList","items":[]}`, nil)
		if matched, err := checkPodEventOnce(context.Background(), "gpu-lab-scheduling-failure", "selector", output); err == nil || matched {
			t.Fatalf("matched=%t, error=%v", matched, err)
		}
	})
	t.Run("malformed events", func(t *testing.T) {
		output := testPodEventOutput(func() string { return testPodJSON("current-uid", testPodCreation) }, `not json`, nil)
		if matched, err := checkPodEventOnce(context.Background(), "gpu-lab-scheduling-failure", "selector", output); err == nil || matched {
			t.Fatalf("matched=%t, error=%v", matched, err)
		}
	})
	t.Run("event missing involved UID", func(t *testing.T) {
		output := testPodEventOutput(func() string { return testPodJSON("current-uid", testPodCreation) }, testEventList("", "Pod", "gpu-lab-scheduling-failure", DemoNamespace, "selector", testPodCreation, testPodCreation), nil)
		if matched, err := checkPodEventOnce(context.Background(), "gpu-lab-scheduling-failure", "selector", output); err == nil || matched {
			t.Fatalf("matched=%t, error=%v", matched, err)
		}
	})
	t.Run("kubectl error", func(t *testing.T) {
		output := testPodEventOutput(func() string { return testPodJSON("current-uid", testPodCreation) }, "", errors.New("kubectl unavailable"))
		if matched, err := checkPodEventOnce(context.Background(), "gpu-lab-scheduling-failure", "selector", output); err == nil || matched {
			t.Fatalf("matched=%t, error=%v", matched, err)
		}
	})
}

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
