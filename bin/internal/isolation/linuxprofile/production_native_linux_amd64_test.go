//go:build linux && amd64

package linuxprofile_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/coder/websocket"
	"golang.org/x/sys/unix"
)

// TestNativeProductionEntry replaces the test process with the installed
// default almagestd entry. A checker is born in a separate cgroup before the
// exec. Thus the service cgroup contains only the daemon when N moves itself
// to its bootstrap child and enables controllers. The checker confirms the
// N-owned listener and then requests a graceful service stop.
func TestNativeProductionEntry(t *testing.T) {
	if os.Getenv("ALMAGEST_PROFILE_PRODUCTION") != "1" {
		t.Skip("requires the disposable production-profile container")
	}
	if os.Getenv("ALMAGEST_PRODUCTION_CHECKER") == "1" {
		runProductionChecker(t)
		return
	}
	const (
		runtimeUID = 65531
		writerUID  = 65530
		helperUID  = 65529
		port       = 48139
	)
	root := "/var/lib/almagest"
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	private := func(path string, uid int) {
		t.Helper()
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, uid, uid); err != nil {
			t.Fatal(err)
		}
	}
	configDir := filepath.Join(root, "config")
	runtimeDir := filepath.Join(root, "runtime")
	private(configDir, writerUID)
	private(runtimeDir, runtimeUID)
	private(filepath.Join(root, "helper"), helperUID)
	private(filepath.Join(root, "broker"), 0)
	configPath := filepath.Join(configDir, "alm.hujson")
	if err := os.WriteFile(configPath, []byte(`{"nodes":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(configPath, writerUID, writerUID); err != nil {
		t.Fatal(err)
	}
	protectedDir := "/qualification/production"
	if err := os.MkdirAll(protectedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	protectedPath := filepath.Join(protectedDir, "protected.hujson")
	protected := fmt.Sprintf(`{"Admin":{"Web":%d,"Unix":""},"System":{"Type":"native","Linux":{}}}`, port)
	if err := os.WriteFile(protectedPath, []byte(protected), 0o600); err != nil {
		t.Fatal(err)
	}

	checkerGroup := "almagest-production-checker-" + strconv.Itoa(os.Getpid())
	if err := os.Mkdir(filepath.Join("/sys/fs/cgroup", checkerGroup), 0o755); err != nil {
		t.Fatal(err)
	}
	checkerFD, err := unix.Open(filepath.Join("/sys/fs/cgroup", checkerGroup), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	checker := exec.Command(os.Args[0], "-test.run=^TestNativeProductionEntry$", "-test.v")
	checker.Env = append(os.Environ(), "ALMAGEST_PRODUCTION_CHECKER=1", "ALMAGEST_PRODUCTION_PARENT="+strconv.Itoa(os.Getpid()))
	checker.Stdout, checker.Stderr = os.Stdout, os.Stderr
	checker.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: checkerFD}
	if err := checker.Start(); err != nil {
		_ = unix.Close(checkerFD)
		t.Fatal(err)
	}
	_ = unix.Close(checkerFD)
	outerBootstrap := filepath.Join("/sys/fs/cgroup", "almagest-production-outer-"+strconv.Itoa(os.Getpid()))
	if err := os.Mkdir(outerBootstrap, 0o755); err != nil {
		_ = checker.Process.Kill()
		t.Fatal(err)
	}
	serviceGroup := "almagest-production-service-" + strconv.Itoa(os.Getpid())
	servicePath := filepath.Join("/sys/fs/cgroup", serviceGroup)
	if err := os.Mkdir(servicePath, 0o755); err != nil {
		_ = checker.Process.Kill()
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outerBootstrap, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		_ = checker.Process.Kill()
		t.Fatal(err)
	}
	if err := os.WriteFile("/sys/fs/cgroup/cgroup.subtree_control", []byte("+cpu +memory +pids\n"), 0); err != nil {
		_ = checker.Process.Kill()
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(servicePath, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		_ = checker.Process.Kill()
		t.Fatal(err)
	}

	argv := []string{"/qualification/almagestd", "--config", configPath, "--protected", protectedPath, "--file-storage", runtimeDir}
	if err := syscall.Exec(argv[0], argv, os.Environ()); err != nil {
		_ = checker.Process.Kill()
		t.Fatal(err)
	}
}

func runProductionChecker(t *testing.T) {
	t.Helper()
	parent, err := strconv.Atoi(os.Getenv("ALMAGEST_PRODUCTION_PARENT"))
	if err != nil || parent < 1 {
		t.Fatalf("invalid production parent %q", os.Getenv("ALMAGEST_PRODUCTION_PARENT"))
	}
	client := &http.Client{Timeout: time.Second}
	ready := false
	for deadline := time.Now().Add(25 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		response, requestErr := client.Get("http://127.0.0.1:48139/")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
	}
	if !ready {
		_ = syscall.Kill(parent, syscall.SIGKILL)
		t.Fatal("production daemon did not publish its admin listener")
	}
	wsCtx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	connection, _, err := websocket.Dial(wsCtx, "ws://127.0.0.1:48139/api/ws", nil)
	if err != nil {
		wsCancel()
		_ = syscall.Kill(parent, syscall.SIGKILL)
		t.Fatal(err)
	}
	_, _, _ = connection.Read(wsCtx) // Initial project-version event.
	hello, err := api.NewRequest("production-status", api.SurfaceAdmin, string(api.MethodAdminHello), api.HelloRequest{ClientName: "production-checker"})
	if err == nil {
		var data []byte
		data, err = api.MarshalMessage(hello)
		if err == nil {
			err = connection.Write(wsCtx, websocket.MessageText, data)
		}
	}
	var status api.HelloResponse
	if err == nil {
		for status.ExecutionMode == "" && err == nil {
			_, data, readErr := connection.Read(wsCtx)
			err = readErr
			if err != nil {
				break
			}
			message, decodeErr := api.UnmarshalMessage(data)
			if decodeErr != nil || message.ID != "production-status" || message.Kind != api.MessageResponse {
				continue
			}
			err = json.Unmarshal(message.Result, &status)
		}
	}
	_ = connection.Close(websocket.StatusNormalClosure, "qualified")
	wsCancel()
	if err != nil || status.ExecutionMode != "isolation" || status.Isolation == nil || status.Isolation.ProtocolVersion == 0 ||
		status.Isolation.RecoveryState != "complete" || status.Isolation.Roles["network"] != "ready" || status.Isolation.Roles["runtime"] != "ready" {
		_ = syscall.Kill(parent, syscall.SIGKILL)
		t.Fatalf("invalid production isolation status: %+v, %v", status, err)
	}
	if err := syscall.Kill(parent, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		if err := syscall.Kill(parent, 0); errors.Is(err, syscall.ESRCH) {
			if os.Getenv("ALMAGEST_PRODUCTION_RESTART") == "" {
				restartProductionEntry(t)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(parent, syscall.SIGKILL)
	t.Fatal("production daemon did not stop after SIGTERM")
}

func restartProductionEntry(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeProductionEntry$", "-test.v", "-test.timeout=80s")
	environment := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "ALMAGEST_PRODUCTION_CHECKER=") || strings.HasPrefix(value, "ALMAGEST_PRODUCTION_PARENT=") ||
			strings.HasPrefix(value, "ALMAGEST_PRODUCTION_RESTART=") {
			continue
		}
		environment = append(environment, value)
	}
	command.Env = append(environment, "ALMAGEST_PRODUCTION_RESTART=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("production service restart: %v\n%s", err, output)
	}
	t.Logf("production service restart passed:\n%s", output)
}
