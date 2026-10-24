// Command snaptwin restores one VM memory snapshot into several clones and
// fails when values that should be unique per instance come back identical.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Spec is the user's snaptwin.json.
type Spec struct {
	Health string  `json:"health"`
	Hook   *Hook   `json:"hook,omitempty"`
	Probes []Probe `json:"probes"`
}

type Hook struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Probe is a GET path whose response must differ between clones.
type Probe struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Clone is what one restored instance answered.
type Clone struct {
	Index      int               `json:"index"`
	HookStatus int               `json:"hook_status,omitempty"`
	Values     map[string]string `json:"values"`
}

// Verdict is the outcome for one probe across all clones.
type Verdict struct {
	Probe  string   `json:"probe"`
	Values []string `json:"values"`
	Unique bool     `json:"unique"`
}

type Report struct {
	Mode     string    `json:"mode"`
	Clones   []Clone   `json:"clones"`
	Verdicts []Verdict `json:"verdicts"`
	Pass     bool      `json:"pass"`
}

func main() {
	var g Guest
	specPath := flag.String("spec", "snaptwin.json", "probe spec")
	clones := flag.Int("clones", 2, "number of clones to restore")
	workDir := flag.String("work", ".snaptwin", "working directory for snapshot and clone logs")
	jsonOut := flag.String("json", "", "write the report as JSON to this file")
	coldBoot := flag.Bool("cold-boot", false, "boot every clone from scratch instead of restoring the snapshot (what ordinary tests do)")
	flag.StringVar(&g.Kernel, "kernel", "", "guest kernel (vmlinux)")
	flag.StringVar(&g.Initrd, "initrd", "", "guest initramfs with the app as /init")
	flag.StringVar(&g.Append, "append", "console=ttyS0 panic=1 ip=10.0.2.15::10.0.2.2:255.255.255.0::eth0:off", "kernel command line")
	flag.IntVar(&g.MemMiB, "mem", 256, "guest memory in MiB")
	flag.IntVar(&g.AppPort, "port", 8080, "port the app listens on inside the guest")
	flag.Parse()

	spec, err := loadSpec(*specPath)
	if err != nil {
		fail(err)
	}
	if g.Kernel == "" || g.Initrd == "" {
		fail(fmt.Errorf("--kernel and --initrd are required"))
	}
	if *clones < 2 {
		fail(fmt.Errorf("--clones must be at least 2"))
	}
	os.RemoveAll(*workDir)

	report := Report{Mode: "snapshot"}
	snapFile := ""
	if *coldBoot {
		report.Mode = "cold-boot"
	} else {
		snapFile, err = filepath.Abs(filepath.Join(*workDir, "snapshot.qemu"))
		if err != nil {
			fail(err)
		}
		if err := buildSnapshot(g, spec, filepath.Join(*workDir, "build"), snapFile); err != nil {
			fail(fmt.Errorf("build snapshot: %w", err))
		}
		info, _ := os.Stat(snapFile)
		fmt.Printf("snapshot  %s (%d MiB, taken after %s answered 200)\n", snapFile, info.Size()>>20, spec.Health)
	}

	for i := 1; i <= *clones; i++ {
		c, err := runClone(g, spec, filepath.Join(*workDir, fmt.Sprintf("clone-%d", i)), snapFile, i)
		if err != nil {
			fail(fmt.Errorf("clone %d: %w", i, err))
		}
		hook := ""
		if spec.Hook != nil {
			hook = fmt.Sprintf(", %s %s -> %d", spec.Hook.Method, spec.Hook.Path, c.HookStatus)
		}
		fmt.Printf("clone %d   %s healthy%s\n", i, report.Mode, hook)
		report.Clones = append(report.Clones, c)
	}

	report.Verdicts, report.Pass = judge(spec, report.Clones)
	printReport(report)
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(report, "", "  ")
		os.WriteFile(*jsonOut, append(b, '\n'), 0o644)
	}
	if !report.Pass {
		os.Exit(1)
	}
}

func loadSpec(path string) (Spec, error) {
	var s Spec
	b, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	if s.Health == "" || len(s.Probes) == 0 {
		return s, fmt.Errorf("%s: health and at least one probe are required", path)
	}
	return s, nil
}

func buildSnapshot(g Guest, spec Spec, dir, snapFile string) error {
	vm, err := startVM(g, dir, "")
	if err != nil {
		return err
	}
	defer vm.Kill()
	if err := waitHealthy(vm.HostPort, spec.Health, 90*time.Second); err != nil {
		return err
	}
	return vm.Snapshot(snapFile)
}

func runClone(g Guest, spec Spec, dir, snapFile string, index int) (Clone, error) {
	c := Clone{Index: index, Values: map[string]string{}}
	vm, err := startVM(g, dir, snapFile)
	if err != nil {
		return c, err
	}
	defer vm.Kill()
	if snapFile != "" {
		if err := vm.Resume(); err != nil {
			return c, err
		}
	}
	if err := waitHealthy(vm.HostPort, spec.Health, 90*time.Second); err != nil {
		return c, err
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", vm.HostPort)
	if spec.Hook != nil {
		req, _ := http.NewRequest(spec.Hook.Method, base+spec.Hook.Path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return c, fmt.Errorf("hook: %w", err)
		}
		resp.Body.Close()
		c.HookStatus = resp.StatusCode
	}
	for _, p := range spec.Probes {
		resp, err := http.Get(base + p.Path)
		if err != nil {
			return c, fmt.Errorf("probe %s: %w", p.Name, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return c, fmt.Errorf("probe %s: status %d", p.Name, resp.StatusCode)
		}
		c.Values[p.Name] = strings.TrimSpace(string(body))
	}
	return c, nil
}

func waitHealthy(port int, path string, timeout time.Duration) error {
	client := http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("%s never answered 200 within %s", path, timeout)
}

// judge marks a probe unique only when every clone returned a distinct value.
func judge(spec Spec, clones []Clone) ([]Verdict, bool) {
	pass := true
	var out []Verdict
	for _, p := range spec.Probes {
		v := Verdict{Probe: p.Name}
		seen := map[string]bool{}
		for _, c := range clones {
			v.Values = append(v.Values, c.Values[p.Name])
			seen[c.Values[p.Name]] = true
		}
		v.Unique = len(seen) == len(clones)
		pass = pass && v.Unique
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return !out[i].Unique && out[j].Unique })
	return out, pass
}

func printReport(r Report) {
	fmt.Println()
	for _, v := range r.Verdicts {
		status := "UNIQUE"
		if !v.Unique {
			status = "SHARED"
		}
		fmt.Printf("%-7s %-16s %s\n", status, v.Probe, strings.Join(v.Values, " | "))
	}
	shared := 0
	for _, v := range r.Verdicts {
		if !v.Unique {
			shared++
		}
	}
	fmt.Println()
	if r.Pass {
		fmt.Printf("PASS  %d/%d probes unique across %d %s clones\n", len(r.Verdicts), len(r.Verdicts), len(r.Clones), r.Mode)
	} else {
		fmt.Printf("FAIL  %d/%d probes returned the same value in more than one %s clone\n", shared, len(r.Verdicts), r.Mode)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "snaptwin:", err)
	os.Exit(2)
}
