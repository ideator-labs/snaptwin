package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// Guest describes what to boot. The same Guest must be used to build the
// snapshot and to restore it, because QEMU only restores into a machine with
// the same device layout.
type Guest struct {
	Kernel  string
	Initrd  string
	Append  string
	MemMiB  int
	AppPort int
}

// VM is one running QEMU process with its QMP socket and a host port
// forwarded to the guest app port.
type VM struct {
	cmd      *exec.Cmd
	qmp      net.Conn
	qmpIn    *bufio.Reader
	HostPort int
	Dir      string
}

// startVM boots the guest, or restores it from snapshotFile when that is set.
func startVM(g Guest, dir string, snapshotFile string) (*VM, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	qmpPath := filepath.Join(dir, "qmp.sock")
	os.Remove(qmpPath)
	args := []string{
		"-M", "q35", "-cpu", "max", "-m", strconv.Itoa(g.MemMiB), "-smp", "1",
		"-nographic", "-nodefaults", "-no-user-config", "-vga", "none",
		"-serial", "file:" + filepath.Join(dir, "console.log"),
		"-kernel", g.Kernel, "-initrd", g.Initrd, "-append", g.Append,
		"-netdev", fmt.Sprintf("user,id=n0,hostfwd=tcp:127.0.0.1:%d-:%d", port, g.AppPort),
		"-device", "virtio-net-pci,netdev=n0,romfile=",
		"-device", "vmgenid,guid=auto",
		"-qmp", "unix:" + qmpPath + ",server=on,wait=off",
	}
	if snapshotFile != "" {
		args = append(args, "-incoming", "file:"+snapshotFile)
	}
	cmd := exec.Command("qemu-system-x86_64", args...)
	logf, err := os.Create(filepath.Join(dir, "qemu.log"))
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start qemu: %w", err)
	}
	vm := &VM{cmd: cmd, HostPort: port, Dir: dir}
	if err := vm.connectQMP(qmpPath); err != nil {
		vm.Kill()
		return nil, err
	}
	return vm, nil
}

func (vm *VM) connectQMP(path string) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.Dial("unix", path)
		if err == nil {
			vm.qmp, vm.qmpIn = c, bufio.NewReader(c)
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("qmp socket never came up: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := vm.qmpIn.ReadBytes('\n'); err != nil {
		return err
	}
	_, err := vm.QMP("qmp_capabilities", nil)
	return err
}

// QMP sends one command and returns its "return" value, skipping async events.
func (vm *VM) QMP(command string, args any) (json.RawMessage, error) {
	req := map[string]any{"execute": command}
	if args != nil {
		req["arguments"] = args
	}
	if err := json.NewEncoder(vm.qmp).Encode(req); err != nil {
		return nil, err
	}
	for {
		line, err := vm.qmpIn.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("qmp %s: %w", command, err)
		}
		var msg struct {
			Return json.RawMessage `json:"return"`
			Error  *struct {
				Desc string `json:"desc"`
			} `json:"error"`
			Event string `json:"event"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			return nil, err
		}
		if msg.Event != "" {
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("qmp %s: %s", command, msg.Error.Desc)
		}
		return msg.Return, nil
	}
}

// Snapshot pauses the guest and writes its full memory and device state.
func (vm *VM) Snapshot(file string) error {
	if _, err := vm.QMP("stop", nil); err != nil {
		return err
	}
	if _, err := vm.QMP("migrate", map[string]any{"uri": "file:" + file}); err != nil {
		return err
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := vm.QMP("query-migrate", nil)
		if err != nil {
			return err
		}
		var st struct {
			Status string `json:"status"`
		}
		json.Unmarshal(raw, &st)
		switch st.Status {
		case "completed":
			return nil
		case "failed", "cancelled":
			return fmt.Errorf("snapshot migration %s", st.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("snapshot timed out")
}

// Resume waits for an incoming snapshot to finish loading, then starts the
// vCPU. The snapshot was taken paused, so QEMU does not start it on its own.
func (vm *VM) Resume() error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := vm.QMP("query-status", nil)
		if err != nil {
			return err
		}
		var st struct {
			Status string `json:"status"`
		}
		json.Unmarshal(raw, &st)
		switch st.Status {
		case "inmigrate":
			time.Sleep(50 * time.Millisecond)
			continue
		case "running":
			return nil
		case "paused", "prelaunch", "postmigrate":
			_, err := vm.QMP("cont", nil)
			return err
		default:
			return fmt.Errorf("restored guest is in state %q", st.Status)
		}
	}
	return fmt.Errorf("snapshot load timed out")
}

func (vm *VM) Kill() {
	if vm.qmp != nil {
		vm.qmp.Close()
	}
	if vm.cmd.Process != nil {
		vm.cmd.Process.Kill()
		vm.cmd.Wait()
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
