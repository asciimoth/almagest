package transport

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

// Keep an explicit table: dispatch and mutation classification are wire contracts.
func TestRequestDispatch(t *testing.T) {
	for _, tc := range []struct {
		op       protocol.Operation
		request  any
		mutation bool
	}{
		{protocol.OperationCapabilities, &protocol.ReadRequest{}, false},
		{protocol.OperationStatus, &protocol.ReadRequest{}, false},
		{protocol.OperationRuntimeReady, &protocol.ReadRequest{}, false},
		{protocol.OperationListInterfaces, &protocol.InterfaceRequest{}, false},
		{protocol.OperationResolverSnapshot, &protocol.ReadRequest{}, false},
		{protocol.OperationCheckTun, &protocol.CheckTunRequest{}, false},
		{protocol.OperationCheckDefaultTun, &protocol.CheckDefaultTunRequest{}, false},
		{protocol.OperationCheckRule, &protocol.CheckRuleRequest{}, false},
		{protocol.OperationCompleteRule, &protocol.CompleteRuleRequest{}, false},
		{protocol.OperationReserveIP, &protocol.ReserveAddressRequest{}, true},
		{protocol.OperationReserveSubnet, &protocol.ReserveAddressRequest{}, true},
		{protocol.OperationCreateTun, &protocol.CreateTunRequest{}, true},
		{protocol.OperationCreateDefaultTun, &protocol.CreateTunRequest{}, true},
		{protocol.OperationSetTunMTU, &protocol.UpdateTunRequest{}, true},
		{protocol.OperationSetTunAddresses, &protocol.UpdateTunRequest{}, true},
		{protocol.OperationSetTunRoutes, &protocol.UpdateTunRequest{}, true},
		{protocol.OperationSetTunName, &protocol.UpdateTunRequest{}, true},
		{protocol.OperationConfigureDNS, &protocol.ConfigureDNSRequest{}, true},
		{protocol.OperationCreateDNSEndpoint, &protocol.CreateDNSEndpointRequest{}, true},
		{protocol.OperationOpenSocket, &protocol.OpenSocketRequest{}, true},
		{protocol.OperationChannel, &protocol.ChannelRequest{}, false},
		{protocol.OperationCreateMatcher, &protocol.CreateMatcherRequest{}, true},
		{protocol.OperationMatch, &protocol.MatchRequest{}, false},
		{protocol.OperationGetTunState, &protocol.ResourceRequest{}, false},
		{protocol.OperationReleaseAllocation, &protocol.ResourceRequest{}, true},
		{protocol.OperationCloseResource, &protocol.ResourceRequest{}, true},
		{protocol.OperationReleaseGraphEpoch, &protocol.ReleaseGraphEpochRequest{}, true},
		{protocol.OperationReadConfig, &protocol.ReadConfigRequest{}, false},
		{protocol.OperationReplaceConfig, &protocol.ReplaceConfigRequest{}, true},
		{protocol.OperationGetOperationResult, &protocol.OperationResultRequest{}, false},
		{protocol.OperationRunNodeCommand, &protocol.NodeCommandRequest{}, true},
		{protocol.OperationRunCallback, &protocol.CallbackRequest{}, true},
	} {
		t.Run(string(tc.op), func(t *testing.T) {
			got, mutation := requestType(tc.op)
			if !tc.op.Valid() || reflect.TypeOf(got) != reflect.TypeOf(tc.request) || mutation != tc.mutation {
				t.Fatalf("dispatch: %T, %v", got, mutation)
			}
			meta := protocol.RequestMeta{RequestID: 999, SessionID: protocol.SessionID{9}, SessionEpoch: 99, GraphEpoch: 7}
			if mutation {
				meta.OperationID = "operation"
			}
			reflect.ValueOf(tc.request).Elem().FieldByName("Meta").Set(reflect.ValueOf(meta))
			body, err := json.Marshal(tc.request)
			if err != nil {
				t.Fatal(err)
			}
			prepared, preparedMutation, err := prepareRequest(tc.op, tc.request)
			if err != nil || preparedMutation != tc.mutation {
				t.Fatalf("prepare pointer: %T, %v, %v", prepared, preparedMutation, err)
			}
			preparedBody, err := marshalPreparedRequest(prepared, preparedMutation, 3, testSession())
			if err != nil {
				t.Fatal(err)
			}
			normalized, err := validateRequest(tc.op, body, 3, testSession(), true)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(preparedBody, normalized) {
				t.Fatalf("prepared body differs: %s != %s", preparedBody, normalized)
			}
			unchanged, err := json.Marshal(tc.request)
			if err != nil || !bytes.Equal(unchanged, body) {
				t.Fatalf("prepare changed caller request: %s, %v", unchanged, err)
			}
			valuePrepared, valueMutation, err := prepareRequest(tc.op, reflect.ValueOf(tc.request).Elem().Interface())
			if err != nil || valueMutation != tc.mutation || reflect.TypeOf(valuePrepared) != reflect.TypeOf(tc.request) {
				t.Fatalf("prepare value: %T, %v, %v", valuePrepared, valueMutation, err)
			}
			if err := Decode(normalized, got); err != nil {
				t.Fatal(err)
			}
			want := meta
			want.RequestID, want.SessionID, want.SessionEpoch = 3, testSession().ID, testSession().Epoch
			if actual := reflect.ValueOf(got).Elem().FieldByName("Meta").Interface(); actual != want {
				t.Fatalf("metadata: %+v", actual)
			}
			if incoming, err := validateRequest(tc.op, normalized, 3, testSession(), false); err != nil || !bytes.Equal(incoming, normalized) {
				t.Fatalf("incoming: %s, %v", incoming, err)
			}
			if _, err := validateRequest(tc.op, normalized, 4, testSession(), false); !errors.Is(err, protocol.ErrInvalidContract) {
				t.Fatalf("wrong request ID: %v", err)
			}
			foreign := testSession()
			foreign.Epoch++
			if _, err := validateRequest(tc.op, normalized, 3, foreign, false); !errors.Is(err, protocol.ErrForeignResource) {
				t.Fatalf("foreign session: %v", err)
			}
			// Flip the operation-ID requirement for each dispatched type.
			if mutation {
				meta.OperationID = ""
			} else {
				meta.OperationID = "unexpected"
			}
			reflect.ValueOf(tc.request).Elem().FieldByName("Meta").Set(reflect.ValueOf(meta))
			body, err = json.Marshal(tc.request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := validateRequest(tc.op, body, 3, testSession(), true); !errors.Is(err, protocol.ErrInvalidContract) {
				t.Fatalf("mutation classification: %v", err)
			}
		})
	}
	for _, op := range []protocol.Operation{protocol.OperationHandshake, "", "ioctl", "STATUS"} {
		if _, err := validateRequest(op, []byte(`{}`), 1, testSession(), true); !errors.Is(err, protocol.ErrUnsupportedOperation) {
			t.Fatal(err)
		}
	}
}

func TestPrepareRequestRejectsOpenOrMismatchedTypes(t *testing.T) {
	type readAlias protocol.ReadRequest
	for _, value := range []any{
		nil,
		(*protocol.ReadRequest)(nil),
		map[string]any{"Meta": protocol.RequestMeta{GraphEpoch: 1}},
		struct{ Meta protocol.RequestMeta }{Meta: protocol.RequestMeta{GraphEpoch: 1}},
		readAlias{Meta: protocol.RequestMeta{GraphEpoch: 1}},
		&readAlias{Meta: protocol.RequestMeta{GraphEpoch: 1}},
		protocol.MatchRequest{},
	} {
		if _, _, err := prepareRequest(protocol.OperationStatus, value); !errors.Is(err, protocol.ErrInvalidContract) {
			t.Fatalf("%T: %v", value, err)
		}
	}
	if _, _, err := prepareRequest("native_ioctl", protocol.ReadRequest{}); !errors.Is(err, protocol.ErrUnsupportedOperation) {
		t.Fatal(err)
	}
}

func FuzzPreparedReadRequestIdentity(f *testing.F) {
	f.Add(uint64(1), "")
	f.Add(uint64(^uint32(0)), "unexpected")
	f.Fuzz(func(t *testing.T, graphEpoch uint64, operationID string) {
		request := protocol.ReadRequest{Meta: protocol.RequestMeta{GraphEpoch: graphEpoch, OperationID: operationID}}
		before := request
		prepared, mutation, err := prepareRequest(protocol.OperationStatus, request)
		if err != nil || mutation {
			t.Fatalf("prepare: %v, %v", mutation, err)
		}
		body, err := marshalPreparedRequest(prepared, mutation, 7, testSession())
		wantValid := graphEpoch != 0 && operationID == ""
		if (err == nil) != wantValid {
			t.Fatalf("marshal validity=%v: %v", wantValid, err)
		}
		if request != before {
			t.Fatal("caller request changed")
		}
		if err == nil {
			if _, err := validateRequest(protocol.OperationStatus, body, 7, testSession(), false); err != nil {
				t.Fatalf("receiver rejected prepared request: %v", err)
			}
		}
	})
}

func FuzzRuntimeReadyRequest(f *testing.F) {
	body, err := json.Marshal(protocol.ReadRequest{Meta: protocol.RequestMeta{
		RequestID: 7, SessionID: testSession().ID, SessionEpoch: testSession().Epoch, GraphEpoch: 1,
	}})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(body)
	f.Add([]byte(`{"Meta":{},"Path":"/host"}`))
	f.Add([]byte(`{"Meta":{},"Meta":{}}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		normalized, err := validateRequest(protocol.OperationRuntimeReady, body, 7, testSession(), false)
		if err != nil {
			return
		}
		var req protocol.ReadRequest
		if err := Decode(normalized, &req); err != nil {
			t.Fatal(err)
		}
		if err := req.Meta.Validate(testSession(), false); err != nil || req.Meta.RequestID != 7 {
			t.Fatalf("accepted invalid identity: %+v, %v", req.Meta, err)
		}
		again, err := validateRequest(protocol.OperationRuntimeReady, normalized, 7, testSession(), false)
		if err != nil || !bytes.Equal(again, normalized) {
			t.Fatalf("unstable readiness request: %s, %v", again, err)
		}
	})
}

func TestJSONBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		want       error
	}{
		{"empty", "", protocol.ErrLimitExceeded},
		{"frame limit", `"` + strings.Repeat("x", protocol.MaxControlFrameBytes-2) + `"`, nil},
		{"frame overflow", `"` + strings.Repeat("x", protocol.MaxControlFrameBytes-1) + `"`, protocol.ErrLimitExceeded},
		{"depth limit", strings.Repeat("[", protocol.MaxControlDepth) + "0" + strings.Repeat("]", protocol.MaxControlDepth), nil},
		{"depth overflow", strings.Repeat("[", protocol.MaxControlDepth+1) + "0" + strings.Repeat("]", protocol.MaxControlDepth+1), protocol.ErrLimitExceeded},
		{"array limit", "[" + strings.Repeat("0,", protocol.MaxAddressOrRouteSetItems-1) + "0]", nil},
		{"array overflow", "[" + strings.Repeat("0,", protocol.MaxAddressOrRouteSetItems) + "0]", protocol.ErrLimitExceeded},
		{"surrogate pair", `"\ud800\udc00"`, nil},
		{"escaped backslash", `"\\ud800"`, nil},
		{"wrong low surrogate", `"\ud800\u0041"`, protocol.ErrInvalidContract},
		{"bad hex", `"\uzzzz"`, protocol.ErrInvalidContract},
		{"short escape", `"\u12"`, protocol.ErrInvalidContract},
		{"truncated escape", `"abc\`, protocol.ErrInvalidContract},
		{"trailing scalar", `{} true`, protocol.ErrInvalidContract},
		{"trailing garbage", `{} x`, protocol.ErrInvalidContract},
		{"broken object", `{"a":`, protocol.ErrInvalidContract},
		{"broken array", `[0,`, protocol.ErrInvalidContract},
		{"nested duplicate", `{"a":[{"b":1,"\u0062":2}]}`, protocol.ErrInvalidContract},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Decode([]byte(tc.data), new(json.RawMessage)); !errors.Is(err, tc.want) {
				t.Fatalf("%v, want %v", err, tc.want)
			}
		})
	}
	// Check tagged, embedded pointer, ignored, and unexported fields.
	type Embedded struct {
		Value int `json:"value"`
	}
	type record struct {
		*Embedded
		Name    string `json:"name,omitempty"`
		Hidden  string `json:"-"`
		private string
	}
	var got record
	if err := Decode([]byte(`{"value":7,"name":"ok"}`), &got); err != nil || got.Embedded == nil || got.Value != 7 || got.Name != "ok" || got.private != "" {
		t.Fatalf("embedded record: %+v, %v", got, err)
	}
	for _, key := range []string{"Value", "Name", "Hidden", "private"} {
		if err := Decode([]byte(`{"`+key+`":0}`), new(record)); !errors.Is(err, protocol.ErrInvalidContract) {
			t.Fatalf("field %q: %v", key, err)
		}
	}
	for _, data := range []string{`[1]`, `[1,2,3]`, `null`, `"12"`} {
		if err := Decode([]byte(data), new([2]byte)); !errors.Is(err, protocol.ErrInvalidContract) {
			t.Fatalf("array %s: %v", data, err)
		}
	}
	if err := Decode([]byte(`[1,2]`), new([2]byte)); err != nil {
		t.Fatal(err)
	}
	if err := Decode([]byte(`{"a":{"value":1}}`), new(map[string]Embedded)); err != nil {
		t.Fatal(err)
	}
	if err := Decode([]byte(`{"a":{"Value":1}}`), new(map[string]Embedded)); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatal(err)
	}
}

type faultStream struct {
	*bytes.Reader
	written                       bytes.Buffer
	readDeadlines, writeDeadlines []time.Time
	deadlineFailAt                int
	writeLimit                    int
	writeErr                      error
	closed                        bool
}

var errTestStream = errors.New("test stream failure")

func (s *faultStream) Close() error { s.closed = true; return nil }
func (s *faultStream) SetReadDeadline(d time.Time) error {
	s.readDeadlines = append(s.readDeadlines, d)
	if len(s.readDeadlines) == s.deadlineFailAt {
		return errTestStream
	}
	return nil
}
func (s *faultStream) SetWriteDeadline(d time.Time) error {
	s.writeDeadlines = append(s.writeDeadlines, d)
	if s.deadlineFailAt != 0 {
		return errTestStream
	}
	return nil
}
func (s *faultStream) Write(b []byte) (int, error) {
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.written.Write(b[:min(len(b), s.writeLimit)])
}

func TestFrameIOFailures(t *testing.T) {
	frame, err := encode(message{Kind: "cancel", ID: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, failAt := range []int{0, 1, 2} {
		s := &faultStream{Reader: bytes.NewReader(frame), deadlineFailAt: failAt}
		err := ReadFrame(s, new(message))
		if failAt != 0 {
			if !errors.Is(err, errTestStream) {
				t.Fatal(err)
			}
			continue
		}
		if err != nil || len(s.readDeadlines) != 2 || !s.readDeadlines[0].IsZero() || s.readDeadlines[1].IsZero() {
			t.Fatalf("read deadlines: %v, %v", s.readDeadlines, err)
		}
	}
	for n := 0; n < len(frame); n++ {
		if err := ReadFrame(memoryStream{bytes.NewReader(frame[:n])}, new(message)); err == nil {
			t.Fatalf("accepted truncation at %d", n)
		}
	}
	for _, tc := range []struct {
		name            string
		limit, deadline int
		failure, want   error
	}{
		{"short writes", 1, 0, nil, nil},
		{"full writes", len(frame), 0, nil, nil},
		{"no progress", 0, 0, nil, io.ErrNoProgress},
		{"write failure", 1, 0, errTestStream, errTestStream},
		{"deadline failure", 1, 1, nil, errTestStream},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &faultStream{writeLimit: tc.limit, deadlineFailAt: tc.deadline, writeErr: tc.failure}
			if err := writeFrame(s, frame); !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			if tc.want == nil && !bytes.Equal(s.written.Bytes(), frame) {
				t.Fatal("short writes changed frame")
			}
			if len(s.writeDeadlines) != 1 || s.writeDeadlines[0].IsZero() {
				t.Fatal("missing write deadline")
			}
		})
	}
	for _, value := range []any{make(chan int), json.RawMessage(`{"a":1,"a":2}`)} {
		if _, err := encode(value); !errors.Is(err, protocol.ErrInvalidContract) {
			t.Fatal(err)
		}
	}
	if _, err := encode(strings.Repeat("x", protocol.MaxControlFrameBytes-6)); err != nil {
		t.Fatal(err)
	}
	if _, err := encode(strings.Repeat("x", protocol.MaxControlFrameBytes-5)); !errors.Is(err, protocol.ErrLimitExceeded) {
		t.Fatal(err)
	}
}

func FuzzStrictJSONRoundTrip(f *testing.F) {
	for _, value := range []string{`{}`, `{"kind":"cancel","id":1}`, `{"kind":"reply","id":2,"body":{"value":"\ud800\udc00"}}`, `{"id":1,"\u0069d":2}`} {
		f.Add([]byte(value))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var first message
		if Decode(data, &first) != nil {
			return
		}
		frame, err := encode(first)
		if errors.Is(err, protocol.ErrLimitExceeded) {
			return
		} // Canonical JSON can be larger.
		if err != nil {
			t.Fatal(err)
		}
		var second message
		if err := ReadFrame(memoryStream{bytes.NewReader(frame)}, &second); err != nil {
			t.Fatal(err)
		}
		canonical, err := encode(second)
		if err != nil || !bytes.Equal(frame, canonical) {
			t.Fatal("canonical frame is not stable")
		}
		if int(binary.BigEndian.Uint32(frame[:4])) != len(frame)-4 {
			t.Fatal("incorrect frame length")
		}
	})
}
