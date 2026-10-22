package inspection

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const DefaultTimeout = 10 * time.Second

type Options struct {
	Mode    Mode
	Timeout time.Duration
}

type smiLog struct {
	DriverVersion string   `xml:"driver_version"`
	GPUs          []smiGPU `xml:"gpu"`
}

type smiGPU struct {
	ID          string `xml:"id,attr"`
	ProductName string `xml:"product_name"`
	UUID        string `xml:"uuid"`
	PCI         struct {
		BusID string `xml:"pci_bus_id"`
	} `xml:"pci"`
	Temperature struct {
		GPU string `xml:"gpu_temp"`
	} `xml:"temperature"`
	Utilization struct {
		GPU string `xml:"gpu_util"`
	} `xml:"utilization"`
	Memory struct {
		Total string `xml:"total"`
		Used  string `xml:"used"`
	} `xml:"fb_memory_usage"`
	ECC struct {
		Volatile  eccSection `xml:"volatile"`
		Aggregate eccSection `xml:"aggregate"`
	} `xml:"ecc_errors"`
}

type eccCounter struct {
	Aggregate string `xml:"aggregate"`
	Total     string `xml:"total"`
	Value     string `xml:",chardata"`
}

type eccSection struct {
	DoubleBit         eccCounter `xml:"double_bit"`
	Total             string     `xml:"total"`
	Aggregate         string     `xml:"aggregate"`
	SRAMUncorrectable eccCounter `xml:"sram_uncorrectable"`
	DRAMUncorrectable eccCounter `xml:"dram_uncorrectable"`
}

func Inspect(ctx context.Context, options Options, dependencies Dependencies) (Report, error) {
	requested := options.Mode
	if requested == "" {
		requested = ModeAuto
	}
	if requested != ModeAuto && requested != ModeReal && requested != ModeSynthetic {
		return Report{}, fmt.Errorf("invalid inspection mode %q", requested)
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < 0 {
		return Report{}, errors.New("inspection timeout must be positive")
	}

	d := dependencies.normalized()
	inspectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var evidence syntheticEvidence
	var tool NvidiaSMI
	selected := requested
	switch requested {
	case ModeReal:
		found, err := FindNvidiaSMI(inspectCtx, d)
		if err != nil {
			report := NewReport(ModeReal, requested, "local nvidia-smi unavailable")
			report.Add("nvidia-smi --version", StatusUnavailable, "", err.Error())
			return report, err
		}
		tool = found
	case ModeSynthetic:
		found, err := detectSyntheticEvidence(inspectCtx, d)
		if err != nil {
			report := NewReport(ModeSynthetic, requested, "synthetic source unavailable")
			report.Add("GPU Lab evidence", StatusUnavailable, "", err.Error())
			return report, err
		}
		if found.Source == "" {
			report := NewReport(ModeSynthetic, requested, "synthetic source unavailable")
			report.Add("GPU Lab evidence", StatusUnavailable, "", "no valid GPU_LAB_STATE_URL or labeled dedicated GPU Lab kubeconfig was found")
			return report, errors.New("synthetic mode requested but no positive GPU Lab evidence was found")
		}
		evidence = found
	case ModeAuto:
		found, err := FindNvidiaSMI(inspectCtx, d)
		if err == nil {
			selected = ModeReal
			tool = found
			break
		}
		if !errors.Is(err, ErrToolNotFound) {
			report := NewReport(ModeReal, requested, "local nvidia-smi probe failed")
			report.Add("nvidia-smi --version", StatusUnavailable, "", err.Error())
			return report, err
		}
		foundEvidence, evidenceErr := detectSyntheticEvidence(inspectCtx, d)
		if evidenceErr != nil {
			report := NewReport(ModeAuto, requested, "GPU Lab detection unavailable")
			report.Add("inspection source", StatusUnavailable, "", evidenceErr.Error())
			return report, evidenceErr
		}
		if foundEvidence.Source == "" {
			report := NewReport(ModeAuto, requested, "no local NVIDIA tool or GPU Lab evidence")
			report.Add("inspection source", StatusUnavailable, "", "no real nvidia-smi or positive GPU Lab evidence was found")
			return report, errors.New("inspection unavailable: no real nvidia-smi or positive GPU Lab evidence")
		}
		selected = ModeSynthetic
		evidence = foundEvidence
	}

	if selected == ModeReal {
		return inspectReal(inspectCtx, requested, tool, d)
	}
	return inspectSynthetic(inspectCtx, requested, evidence, d)
}

func inspectReal(ctx context.Context, requested Mode, tool NvidiaSMI, d Dependencies) (Report, error) {
	report := NewReport(ModeReal, requested, tool.Path)
	report.Add("nvidia-smi --version", StatusPass, tool.Version, "local official tool selected after shim detection")
	data, _, err := d.RunCommand(ctx, tool.Path, "-q", "-x")
	if err != nil {
		report.Add("nvidia-smi -q -x", StatusUnavailable, "", err.Error())
		return report, fmt.Errorf("collect real GPU data with nvidia-smi -q -x: %w", err)
	}
	parsed, err := parseSMI(data)
	if err != nil {
		report.Add("nvidia-smi XML", StatusUnavailable, "", err.Error())
		return report, fmt.Errorf("parse nvidia-smi XML: %w", err)
	}
	addRealChecks(&report, parsed)
	addDCGMICheck(ctx, &report, d)
	return report, nil
}

func parseSMI(data []byte) (smiLog, error) {
	var parsed smiLog
	if err := xml.Unmarshal(data, &parsed); err != nil {
		return smiLog{}, err
	}
	if len(parsed.GPUs) == 0 {
		return smiLog{}, errors.New("nvidia-smi XML contains no GPU entries")
	}
	return parsed, nil
}

func addRealChecks(report *Report, parsed smiLog) {
	if isUnavailableText(parsed.DriverVersion) {
		report.Add("driver version", missingStatus(parsed.DriverVersion), "", "driver version was not reported")
	} else {
		report.Add("driver version", StatusPass, strings.TrimSpace(parsed.DriverVersion), "reported by nvidia-smi")
	}
	for index, gpu := range parsed.GPUs {
		id := fmt.Sprintf("GPU %d", index)
		identityParts := make([]string, 0, 4)
		for _, part := range []struct{ label, value string }{
			{"product", gpu.ProductName}, {"UUID", gpu.UUID}, {"PCI", gpu.PCI.BusID}, {"GPU ID", gpu.ID},
		} {
			if !isUnavailableText(part.value) {
				identityParts = append(identityParts, part.label+"="+strings.TrimSpace(part.value))
			}
		}
		if len(identityParts) == 0 {
			report.Add(id+" identity", StatusUnavailable, "", "nvidia-smi reported no product name, UUID, PCI bus ID, or GPU ID")
		} else {
			report.Add(id+" identity", StatusPass, strings.Join(identityParts, " "), "reported by nvidia-smi")
		}
		addXMLMeasurement(report, id+" temperature", gpu.Temperature.GPU, "°C", "display only; no generic temperature threshold applied")
		addXMLMeasurement(report, id+" utilization", gpu.Utilization.GPU, "%", "reported utilization")
		addXMLMemory(report, id, gpu.Memory.Used, gpu.Memory.Total)
		addXMLECC(report, id+" volatile uncorrected ECC (since last driver load)", gpu.ECC.Volatile, "volatile")
		addXMLECC(report, id+" aggregate uncorrected ECC (lifetime history)", gpu.ECC.Aggregate, "aggregate")
	}
}

func addXMLMeasurement(report *Report, name, raw, unit, detail string) {
	value, state := parseXMLNumber(raw)
	if state != StatusPass {
		report.Add(name, state, "", fieldReason(raw))
		return
	}
	if nameHasSuffix(name, "utilization") && (value < 0 || value > 100) {
		report.Add(name, StatusUnavailable, strings.TrimSpace(raw), "value is outside the valid 0–100 percent range")
		return
	}
	report.Add(name, StatusPass, strings.TrimSpace(raw), detail)
	_ = unit
}

func addXMLMemory(report *Report, gpu, used, total string) {
	usedValue, usedStatus := parseXMLNumber(used)
	totalValue, totalStatus := parseXMLNumber(total)
	name := gpu + " memory used / total"
	if usedStatus != StatusPass || totalStatus != StatusPass {
		state := combineMissingStatus(usedStatus, totalStatus)
		report.Add(name, state, "", "nvidia-smi did not report valid used and total memory values")
		return
	}
	if usedValue < 0 || totalValue <= 0 || usedValue > totalValue {
		report.Add(name, StatusUnavailable, strings.TrimSpace(used)+" / "+strings.TrimSpace(total), "memory values are invalid or inconsistent")
		return
	}
	report.Add(name, StatusPass, strings.TrimSpace(used)+" / "+strings.TrimSpace(total), "reported framebuffer memory")
}

func addXMLECC(report *Report, name string, section eccSection, history string) {
	if raw := firstPresent(section.DoubleBit.Total, section.DoubleBit.Aggregate, section.Total, section.Aggregate); raw != "" {
		addECCCount(report, name, raw, history)
		return
	}
	sram := eccCounterValue(section.SRAMUncorrectable)
	dram := eccCounterValue(section.DRAMUncorrectable)
	if sram == "" && dram == "" {
		report.Add(name, StatusUnavailable, "", "uncorrected ECC counter was not reported")
		return
	}
	if sram == "" || dram == "" {
		report.Add(name, StatusUnavailable, "SRAM="+displayMissing(sram)+", DRAM="+displayMissing(dram), "modern SRAM/DRAM counters are incomplete")
		return
	}
	sramValue, sramStatus := parseXMLNumber(sram)
	dramValue, dramStatus := parseXMLNumber(dram)
	if sramStatus != StatusPass || dramStatus != StatusPass || sramValue < 0 || dramValue < 0 || math.Trunc(sramValue) != sramValue || math.Trunc(dramValue) != dramValue {
		report.Add(name, StatusUnavailable, "SRAM="+sram+", DRAM="+dram, "modern SRAM/DRAM counters are invalid")
		return
	}
	value := "SRAM=" + strings.TrimSpace(sram) + ", DRAM=" + strings.TrimSpace(dram)
	if sramValue+dramValue > 0 {
		report.Add(name, StatusWarn, value, history+" uncorrected ECC counters record history and do not establish a current fault")
		return
	}
	report.Add(name, StatusPass, value, history+" counters are zero; this is not a current-fault test")
}

func addECCCount(report *Report, name, raw, history string) {
	value, status := parseXMLNumber(raw)
	if status != StatusPass {
		report.Add(name, status, "", fieldReason(raw))
		return
	}
	if value < 0 || math.Trunc(value) != value {
		report.Add(name, StatusUnavailable, strings.TrimSpace(raw), "ECC count is not a non-negative integer")
		return
	}
	if value == 0 {
		report.Add(name, StatusPass, strings.TrimSpace(raw), history+" counter reported as zero; this is not a current-fault test")
		return
	}
	report.Add(name, StatusWarn, strings.TrimSpace(raw), history+" uncorrected ECC count records history and does not establish a current fault")
}

func eccCounterValue(counter eccCounter) string {
	return firstPresent(counter.Total, counter.Aggregate, counter.Value)
}

func firstPresent(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func displayMissing(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unavailable"
	}
	return strings.TrimSpace(value)
}

func parseXMLNumber(raw string) (float64, Status) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, StatusUnavailable
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return 0, StatusUnavailable
	}
	if strings.EqualFold(fields[0], "n/a") || strings.EqualFold(fields[0], "not supported") {
		return 0, StatusUnsupported
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, StatusUnavailable
	}
	return value, StatusPass
}

func fieldReason(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "field was not reported"
	}
	if strings.EqualFold(strings.TrimSpace(raw), "n/a") || strings.EqualFold(strings.TrimSpace(raw), "not supported") {
		return "tool reports this field as unsupported"
	}
	return "field is not a finite valid number"
}

func isUnavailableText(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || strings.EqualFold(value, "n/a") || strings.EqualFold(value, "not supported")
}

func missingStatus(value string) Status {
	if strings.EqualFold(strings.TrimSpace(value), "n/a") || strings.EqualFold(strings.TrimSpace(value), "not supported") {
		return StatusUnsupported
	}
	return StatusUnavailable
}

func combineMissingStatus(left, right Status) Status {
	if left == StatusUnavailable || right == StatusUnavailable {
		return StatusUnavailable
	}
	return StatusUnsupported
}

func nameHasSuffix(name, suffix string) bool { return strings.HasSuffix(name, suffix) }

func addDCGMICheck(ctx context.Context, report *Report, d Dependencies) {
	path, err := FindExecutable("dcgmi", d)
	if errors.Is(err, ErrToolNotFound) {
		report.Add("DCGM discovery", StatusUnsupported, "", "dcgmi is not installed")
		return
	}
	if err != nil {
		report.Add("DCGM discovery", StatusUnavailable, "", err.Error())
		return
	}
	stdout, _, err := d.RunCommand(ctx, path, "discovery", "-l")
	if err != nil {
		report.Add("DCGM discovery", StatusUnavailable, "", err.Error())
		return
	}
	value := compactOutput(stdout, 360)
	if value == "" {
		report.Add("DCGM discovery", StatusUnavailable, "", "dcgmi discovery -l returned no device listing")
		return
	}
	if strings.Contains(strings.ToLower(value), "no gpus found") || strings.Contains(strings.ToLower(value), "no devices found") {
		report.Add("DCGM discovery", StatusUnavailable, value, "dcgmi reported no listed devices")
		return
	}
	report.Add("DCGM discovery", StatusPass, value, "device listing only; diagnostics were not run")
}

func compactOutput(data []byte, limit int) string {
	value := strings.Join(strings.Fields(string(data)), " ")
	if len(value) > limit {
		return value[:limit] + "…"
	}
	return value
}
