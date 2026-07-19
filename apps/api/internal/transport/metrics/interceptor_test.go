package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/iFTY-R/game-night/apps/api/internal/transport/logging"
	identityv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1/identityv1connect"
)

func TestUnaryInterceptorRecordsBoundedResultWithoutRequestData(t *testing.T) {
	var output bytes.Buffer
	observer := &rpcObservation{}
	interceptor, err := NewUnaryInterceptor(
		slog.New(logging.NewJSONHandler(&output, slog.LevelInfo)),
		observer,
		identityv1connect.IdentityServiceChangeUsernameProcedure,
	)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(connect.NewUnaryHandler(
		identityv1connect.IdentityServiceChangeUsernameProcedure,
		func(context.Context, *connect.Request[identityv1.ChangeUsernameRequest]) (*connect.Response[identityv1.ChangeUsernameResponse], error) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("private validation detail"))
		},
		connect.WithInterceptors(interceptor),
	))
	t.Cleanup(server.Close)

	client := connect.NewClient[identityv1.ChangeUsernameRequest, identityv1.ChangeUsernameResponse](
		server.Client(),
		server.URL+identityv1connect.IdentityServiceChangeUsernameProcedure,
	)
	request := connect.NewRequest(&identityv1.ChangeUsernameRequest{Username: "private-username"})
	request.Header().Set("Cookie", "device-secret-cookie")
	if _, callErr := client.CallUnary(t.Context(), request); connect.CodeOf(callErr) != connect.CodeInvalidArgument {
		t.Fatalf("RPC error = %v, want invalid_argument", callErr)
	}

	if observer.operation != identityv1connect.IdentityServiceChangeUsernameProcedure || observer.result != "invalid_argument" || observer.duration < 0 {
		t.Fatalf("observation = %+v", observer)
	}
	for _, privateValue := range []string{"private-username", "device-secret-cookie", "private validation detail"} {
		if strings.Contains(output.String(), privateValue) {
			t.Fatalf("RPC log leaked %q: %s", privateValue, output.String())
		}
	}
	assertRPCLogShape(t, output.Bytes(), identityv1connect.IdentityServiceChangeUsernameProcedure, "invalid_argument")
}

func TestUnaryInterceptorFoldsUnknownProcedure(t *testing.T) {
	const unreviewedProcedure = "/attacker.Service/InjectedValue"
	var output bytes.Buffer
	observer := &rpcObservation{}
	interceptor, err := NewUnaryInterceptor(
		slog.New(logging.NewJSONHandler(&output, slog.LevelInfo)),
		observer,
		identityv1connect.IdentityServiceGetCurrentIdentityProcedure,
	)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(connect.NewUnaryHandler(
		unreviewedProcedure,
		func(context.Context, *connect.Request[identityv1.GetCurrentIdentityRequest]) (*connect.Response[identityv1.GetCurrentIdentityResponse], error) {
			return connect.NewResponse(&identityv1.GetCurrentIdentityResponse{}), nil
		},
		connect.WithInterceptors(interceptor),
	))
	t.Cleanup(server.Close)
	client := connect.NewClient[identityv1.GetCurrentIdentityRequest, identityv1.GetCurrentIdentityResponse](server.Client(), server.URL+unreviewedProcedure)
	if _, callErr := client.CallUnary(t.Context(), connect.NewRequest(&identityv1.GetCurrentIdentityRequest{})); callErr != nil {
		t.Fatal(callErr)
	}

	if observer.operation != ResultUnknown || observer.result != "ok" {
		t.Fatalf("observation = %+v", observer)
	}
	if strings.Contains(output.String(), unreviewedProcedure) {
		t.Fatalf("RPC log retained an unreviewed procedure: %s", output.String())
	}
	assertRPCLogShape(t, output.Bytes(), ResultUnknown, "ok")
}

func assertRPCLogShape(t testing.TB, record []byte, wantOperation, wantResult string) {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(record, &decoded); err != nil {
		t.Fatalf("decode RPC log: %v", err)
	}
	for _, key := range []string{"time", "level", "msg", "operation", "result", "duration_ms"} {
		if _, exists := decoded[key]; !exists {
			t.Fatalf("RPC log omitted %q: %s", key, record)
		}
	}
	if len(decoded) != 6 || decoded["operation"] != wantOperation || decoded["result"] != wantResult {
		t.Fatalf("RPC log fields = %#v", decoded)
	}
}

type rpcObservation struct {
	operation string
	result    string
	duration  time.Duration
}

func (observation *rpcObservation) ObserveRPC(operation, result string, duration time.Duration) {
	observation.operation = operation
	observation.result = result
	observation.duration = duration
}
