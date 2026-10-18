package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	sandbox "github.com/asciimoth/gobox"
)

func serviceConfig(t *testing.T) policy.Config {
	t.Helper()
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return policy.Config{Mode: sandbox.Off, TemporaryRoot: filepath.Join(root, "temporary"), RegistryRoot: filepath.Join(root, "registry"), Programs: map[sandbox.ProgramID]policy.Program{"helper": {Executable: executable, Builder: "fixed-v1"}}, Templates: map[sandbox.TemplateID]policy.Template{"test": {AllowedClasses: []string{"test/Node"}, Programs: []sandbox.ProgramID{"helper"}, RuntimeProfile: "direct-v1", PersistentRoot: filepath.Join(root, "persistent")}}}
}
func serviceProbe() policy.Probe {
	return policy.Probe{Platform: runtime.GOOS, EffectiveIdentity: "test", Available: map[sandbox.Feature]bool{}}
}

func TestServiceDurableOwnerEpochAndRename(t *testing.T) {
	config := serviceConfig(t)
	service, err := NewDirect(config, serviceProbe())
	if err != nil {
		t.Fatal(err)
	}
	proposed := sandbox.Owner{Workspace: "main", Class: "test/Node", Name: "First"}
	owner, view, err := service.IssueAndBindOwner(context.Background(), proposed)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Epoch != 1 || owner.NameRevision != 1 || owner.Key == "" || view == nil {
		t.Fatal(owner)
	}
	renamed, err := service.RebindOwner(context.Background(), sandbox.OwnerRename{Owner: owner, NewName: "Second", OperationID: "rename-1"})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Key != owner.Key || renamed.Epoch != owner.Epoch || renamed.NameRevision != 2 {
		t.Fatal(renamed)
	}
	if err = service.ReleaseOwner(context.Background(), renamed); err != nil {
		t.Fatal(err)
	}
	if err = service.Close(); err != nil {
		t.Fatal(err)
	}

	service, err = NewDirect(config, serviceProbe())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := service.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	})
	restored := renamed
	next, _, err := service.IssueAndBindOwner(context.Background(), restored)
	if err != nil {
		t.Fatal(err)
	}
	if next.Key != owner.Key || next.Epoch != owner.Epoch+1 || next.NameRevision != renamed.NameRevision+1 {
		t.Fatalf("next=%+v", next)
	}
	if _, err = service.BindOwner(context.Background(), sandbox.Owner{Workspace: "main", Key: "guessed", Epoch: 1, Class: "test/Node", Name: "Second", NameRevision: 1}); !errors.Is(err, sandbox.ErrDenied) {
		t.Fatalf("guessed owner: %v", err)
	}
}

func TestDiscoveryFailureIsScopedToAffectedCapability(t *testing.T) {
	config := serviceConfig(t)
	config.UnavailableTemplates = map[sandbox.TemplateID]string{"tor": "Tor was not found in the host search path"}
	config.UnavailablePrograms = map[sandbox.ProgramID]string{"obfs4": "lyrebird has a missing runtime library"}
	service, err := NewDirect(config, serviceProbe())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	_, view, err := service.IssueAndBindOwner(context.Background(), sandbox.Owner{Workspace: "main", Class: "test/Node", Name: "Node"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = view.Acquire(context.Background(), "tor", "tor"); !errors.Is(err, sandbox.ErrUnavailable) || !strings.Contains(err.Error(), "Tor was not found") {
		t.Fatalf("Tor acquisition error = %v", err)
	}
	instance, err := view.Acquire(context.Background(), "working", "test")
	if err != nil {
		t.Fatalf("unrelated template failed: %v", err)
	}
	if _, err = instance.ProgramPath("obfs4"); !errors.Is(err, sandbox.ErrUnavailable) || !strings.Contains(err.Error(), "missing runtime library") {
		t.Fatalf("obfs4 path error = %v", err)
	}
	if path, pathErr := instance.ProgramPath("helper"); pathErr != nil || path == "" {
		t.Fatalf("unrelated program path = %q, %v", path, pathErr)
	}
}

func TestAutoMappedTemplateRemainsRunnableWithoutExactView(t *testing.T) {
	config := serviceConfig(t)
	config.Mode = sandbox.Auto
	source := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(source, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	mapped := config.Templates["test"]
	mapped.PersistentRoot = ""
	mapped.ReadOnly = []policy.ReadOnly{{Source: source, Target: "/mapped/input"}}
	config.Templates["mapped"] = mapped
	service, err := NewDirect(config, serviceProbe())
	if err != nil {
		t.Fatalf("auto service rejected mapped template: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	_, view, err := service.IssueAndBindOwner(context.Background(), sandbox.Owner{Workspace: "main", Class: "test/Node", Name: "Node"})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := view.Acquire(context.Background(), "mapped", "mapped")
	if err != nil {
		t.Fatalf("mapped template acquisition failed: %v", err)
	}
	if path, pathErr := instance.ProgramPath("helper"); pathErr != nil || path == "" {
		t.Fatalf("mapped template program path = %q, %v", path, pathErr)
	}
	if err = instance.Close(); err != nil {
		t.Fatalf("close mapped template: %v", err)
	}
}

func TestServiceGracefulRestartRetainsAuthorizedState(t *testing.T) {
	config := serviceConfig(t)
	service, err := NewDirect(config, serviceProbe())
	if err != nil {
		t.Fatal(err)
	}
	owner, view, err := service.IssueAndBindOwner(context.Background(), sandbox.Owner{Workspace: "main", Class: "test/Node", Name: "Node"})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := view.Acquire(context.Background(), "main", "test")
	if err != nil {
		t.Fatal(err)
	}
	files, err := instance.FS(sandbox.Persistent)
	if err != nil {
		t.Fatal(err)
	}
	if err = files.WriteFile(context.Background(), "retained", []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = service.Close(); err != nil {
		t.Fatal(err)
	}

	service, err = NewDirect(config, serviceProbe())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := service.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	})
	restored, view, err := service.IssueAndBindOwner(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Key != owner.Key || restored.Epoch != owner.Epoch+1 || restored.NameRevision != owner.NameRevision+1 {
		t.Fatalf("restored owner = %+v", restored)
	}
	instance, err = view.Acquire(context.Background(), "main", "test")
	if err != nil {
		t.Fatal(err)
	}
	files, err = instance.FS(sandbox.Persistent)
	if err != nil {
		t.Fatal(err)
	}
	data, err := files.ReadFile(context.Background(), "retained", 16)
	if err != nil || string(data) != "state" {
		t.Fatalf("retained state = %q, %v", data, err)
	}
}

func TestRegistryQuarantinesUncleanActiveOwner(t *testing.T) {
	root := t.TempDir()
	registry, err := openRegistry(root, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = registry.issue(sandbox.Owner{Workspace: "main", Class: "test/Node", Name: "Node"}); err != nil {
		t.Fatal(err)
	}
	if _, err = openRegistry(root, 8); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatalf("concurrent registry reopened: %v", err)
	}
	if err = registry.closeLock(); err != nil {
		t.Fatal(err)
	}
	recovered, err := openRegistry(root, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recovered.closeLock() })
	var quarantinedRecord ownerRecord
	for _, record := range recovered.state.Owners {
		quarantinedRecord = record
	}
	if quarantinedRecord.Active || !quarantinedRecord.Orphan || !quarantinedRecord.Quarantined {
		t.Fatalf("owner was not quarantined: %+v", quarantinedRecord)
	}
	if _, issueErr := recovered.issue(quarantinedRecord.Owner); !errors.Is(issueErr, sandbox.ErrUnavailable) || !errors.Is(issueErr, sandbox.ErrStale) {
		t.Fatalf("quarantined owner issue = %v", issueErr)
	}
	active, orphaned, quarantined := recovered.counts()
	if active != 0 || orphaned != 1 || quarantined != 1 {
		t.Fatalf("recovery counts = active %d, orphaned %d, quarantined %d", active, orphaned, quarantined)
	}
	replacement, issueErr := recovered.issue(sandbox.Owner{
		Workspace: quarantinedRecord.Owner.Workspace,
		Class:     quarantinedRecord.Owner.Class,
		Name:      quarantinedRecord.Owner.Name,
	})
	if issueErr != nil {
		t.Fatalf("replacement owner issue: %v", issueErr)
	}
	if replacement.Key == quarantinedRecord.Owner.Key {
		t.Fatal("replacement reused the quarantined owner key")
	}
	if releaseErr := recovered.release(replacement); releaseErr != nil {
		t.Fatalf("release replacement owner: %v", releaseErr)
	}
	if _, err = recovered.issue(sandbox.Owner{Workspace: "main", Class: "test/Node", Name: "Other"}); err != nil {
		t.Fatalf("unrelated owner issue: %v", err)
	}
}

func TestRegistryRejectsAliasedOrMalformedState(t *testing.T) {
	t.Run("hard link", func(t *testing.T) {
		root := t.TempDir()
		registry, err := openRegistry(root, 8)
		if err != nil {
			t.Fatal(err)
		}
		owner, err := registry.issue(sandbox.Owner{Workspace: "main", Class: "test/Node", Name: "Node"})
		if err != nil {
			t.Fatal(err)
		}
		if err = registry.release(owner); err != nil {
			t.Fatal(err)
		}
		if err = registry.closeLock(); err != nil {
			t.Fatal(err)
		}
		if err = os.Link(filepath.Join(root, "owners-v1.json"), filepath.Join(root, "registry-alias")); err != nil {
			t.Fatal(err)
		}
		if _, err = openRegistry(root, 8); !errors.Is(err, sandbox.ErrDenied) {
			t.Fatalf("aliased registry error = %v", err)
		}
	})

	t.Run("key mismatch", func(t *testing.T) {
		root := t.TempDir()
		registry, err := openRegistry(root, 8)
		if err != nil {
			t.Fatal(err)
		}
		owner, err := registry.issue(sandbox.Owner{Workspace: "main", Class: "test/Node", Name: "Node"})
		if err != nil {
			t.Fatal(err)
		}
		if err = registry.release(owner); err != nil {
			t.Fatal(err)
		}
		record := registry.state.Owners[owner.Key]
		delete(registry.state.Owners, owner.Key)
		registry.state.Owners["different-key"] = record
		if err = registry.save(); err != nil {
			t.Fatal(err)
		}
		if err = registry.closeLock(); err != nil {
			t.Fatal(err)
		}
		if _, err = openRegistry(root, 8); !errors.Is(err, sandbox.ErrInvalid) {
			t.Fatalf("malformed registry error = %v", err)
		}
	})
}

func TestIssueAndBindCancellationRollsBackOwner(t *testing.T) {
	config := serviceConfig(t)
	service, err := NewDirect(config, serviceProbe())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := service.Close(); closeErr != nil {
			t.Errorf("Close: %v", closeErr)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	proposed := sandbox.Owner{Workspace: "main", Class: "test/Node", Name: "Node"}
	if _, _, err = service.IssueAndBindOwner(ctx, proposed); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	service.registry.mu.Lock()
	defer service.registry.mu.Unlock()
	for _, record := range service.registry.state.Owners {
		if record.Active {
			t.Fatal("canceled binding left an active owner")
		}
	}
}

func TestRegistryPersistenceFailureRollsBackMemory(t *testing.T) {
	registry, err := openRegistry(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.closeLock() })
	owner, err := registry.issue(sandbox.Owner{Workspace: "main", Class: "test/Node", Name: "First"})
	if err != nil {
		t.Fatal(err)
	}
	want := registry.state.Owners[owner.Key]
	registry.save = func() error { return errors.New("injected persistence failure") }
	if _, err = registry.rename(sandbox.OwnerRename{Owner: owner, NewName: "Second", OperationID: "rename-failure"}); err == nil {
		t.Fatal("rename succeeded despite persistence failure")
	}
	if got := registry.state.Owners[owner.Key]; got != want {
		t.Fatalf("owner changed after failed rename: got %+v, want %+v", got, want)
	}
	if len(registry.state.Renames) != 0 {
		t.Fatalf("failed rename left receipt: %+v", registry.state.Renames)
	}
	if err = registry.release(owner); err == nil {
		t.Fatal("release succeeded despite persistence failure")
	}
	if got := registry.state.Owners[owner.Key]; got != want {
		t.Fatalf("owner changed after failed release: got %+v, want %+v", got, want)
	}
}

func TestOwnerDeletionPolicyAndShutdownRetention(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		delete     bool
		useRelease bool
		wantFiles  bool
	}{
		{name: "explicit deletion purges", delete: true, useRelease: true},
		{name: "explicit retention keeps", useRelease: true, wantFiles: true},
		{name: "service shutdown keeps", delete: true, wantFiles: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			config := serviceConfig(t)
			template := config.Templates["test"]
			template.DeleteStateOnOwnerRemoval = testCase.delete
			config.Templates["test"] = template
			service, err := NewDirect(config, serviceProbe())
			if err != nil {
				t.Fatal(err)
			}
			owner, view, err := service.IssueAndBindOwner(context.Background(), sandbox.Owner{Workspace: "main", Class: "test/Node", Name: "Node"})
			if err != nil {
				t.Fatal(err)
			}
			instance, err := view.Acquire(context.Background(), "main", "test")
			if err != nil {
				t.Fatal(err)
			}
			files, err := instance.FS(sandbox.Persistent)
			if err != nil {
				t.Fatal(err)
			}
			if err = files.WriteFile(context.Background(), "marker", []byte("state"), 0o600); err != nil {
				t.Fatal(err)
			}
			if testCase.useRelease {
				err = service.ReleaseOwner(context.Background(), owner)
			} else {
				err = service.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			found := false
			if err = filepath.WalkDir(template.PersistentRoot, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr == nil && entry.Name() == "marker" {
					found = true
				}
				return walkErr
			}); err != nil {
				t.Fatal(err)
			}
			if found != testCase.wantFiles {
				t.Fatalf("marker present = %t, want %t", found, testCase.wantFiles)
			}
			if testCase.useRelease {
				if err = service.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
