package verification

import (
	"fmt"
	"strings"
)

// One instant query checks phase and readings together. Separate polling
// checks could accidentally combine values from different timeline phases.
func thermalTimelineQuery() string {
	selectors := `{__name__=~"gpu_lab_gpu_(temperature_celsius|utilization_percent|power_watts|throttle_active|memory_used_bytes|memory_total_bytes)"}`
	guards := fmt.Sprintf(`(count(%s) == 144) and (count((%s) < +Inf) == 144) and (count((%s) > -Inf) == 144) and (min(time() - timestamp(%s)) >= 0) and (max(time() - timestamp(%s)) <= 30)`, selectors, selectors, selectors, selectors, selectors)
	var phases []string
	for _, phase := range []struct {
		name                                              string
		temperature, utilization, power, memory, throttle int
	}{
		{"baseline", 45, 15, 80, 20, 0},
		{"warning", 82, 88, 210, 60, 0},
		{"critical", 96, 62, 245, 74, 1},
		{"recovery", 45, 15, 80, 20, 0},
	} {
		selector := fmt.Sprintf(`gpu_lab_scenario_phase{scenario="thermal-escalation",phase="%s"}`, phase.name)
		checks := []string{
			fmt.Sprintf(`(count(%s == 1) == 3)`, selector),
			fmt.Sprintf(`(count(count by (generation) (%s)) == 1)`, selector),
			fmt.Sprintf(`(min(time() - timestamp(%s)) >= 0) and (max(time() - timestamp(%s)) <= 30)`, selector, selector),
			guards,
			fmt.Sprintf(`(max(abs(100 * gpu_lab_gpu_memory_used_bytes / gpu_lab_gpu_memory_total_bytes - %d)) < 0.000001)`, phase.memory),
		}
		for _, metric := range []struct {
			name  string
			value int
		}{
			{"temperature_celsius", phase.temperature},
			{"utilization_percent", phase.utilization},
			{"power_watts", phase.power},
			{"throttle_active", phase.throttle},
		} {
			checks = append(checks, fmt.Sprintf(`(count(gpu_lab_gpu_%s == %d) == 24)`, metric.name, metric.value))
		}
		phases = append(phases, "("+strings.Join(checks, " and ")+")")
	}
	return strings.Join(phases, " or ")
}
