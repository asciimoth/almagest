package storage

import "testing"

func TestKValueCloneCopiesData(t *testing.T) {
	value := KValue{Data: []byte("secret"), Hidden: true, String: true}
	clone := value.Clone()
	value.Data[0] = 'x'

	if string(clone.Data) != "secret" {
		t.Fatalf("clone data = %q, want secret", string(clone.Data))
	}
	if !clone.Hidden || !clone.String {
		t.Fatalf("clone flags = hidden:%v string:%v, want true true", clone.Hidden, clone.String)
	}
}
