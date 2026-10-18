package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAllBboltWritesUseStorageBarrier(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(entry.Name()) //nolint:gosec // Names come from the fixed package directory.
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), data, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if ok && entry.Name() == "kv_bbolt.go" &&
				(fn.Name.Name == "update" || fn.Name.Name == "updateDuringSnapshot") {
				continue
			}
			ast.Inspect(declaration, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				method, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || method.Sel.Name != "Update" {
					return true
				}
				if field, ok := method.X.(*ast.SelectorExpr); ok && field.Sel.Name == "db" {
					t.Errorf("%s bypasses the snapshot storage-writer barrier", entry.Name())
				}
				return true
			})
		}
	}
}

func TestStorageWriteBarrierQuiescesAndResumesWriters(t *testing.T) {
	barrier := newStorageWriteBarrier()
	endFirst, err := barrier.beginWrite(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	quiesced := make(chan func(), 1)
	go func() {
		resume, acquireErr := barrier.quiesce(context.Background())
		if acquireErr == nil {
			quiesced <- resume
		}
	}()
	select {
	case <-quiesced:
		t.Fatal("snapshot passed an active writer")
	case <-time.After(20 * time.Millisecond):
	}

	second := make(chan func(), 1)
	go func() {
		end, acquireErr := barrier.beginWrite(context.Background())
		if acquireErr == nil {
			second <- end
		}
	}()
	endFirst()
	resume := <-quiesced
	select {
	case <-second:
		t.Fatal("writer passed an active snapshot")
	case <-time.After(20 * time.Millisecond):
	}
	resume()
	endSecond := <-second
	endSecond()
}

func TestStorageWriteBarrierCancellationDoesNotStrandWriters(t *testing.T) {
	barrier := newStorageWriteBarrier()
	end, err := barrier.beginWrite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := barrier.quiesce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("quiesce error = %v, want context cancellation", err)
	}
	end()
	next, err := barrier.beginWrite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next()
}

func TestStorageWriteBarrierCanceledWriter(t *testing.T) {
	barrier := newStorageWriteBarrier()
	resume, err := barrier.quiesce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := barrier.beginWrite(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("writer error = %v, want deadline", err)
	}
	resume()
}
