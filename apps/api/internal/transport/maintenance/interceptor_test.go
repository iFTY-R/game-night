package maintenance

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	identityv1 "github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1"
	"github.com/iFTY-R/game-night/contracts/gen/go/platform/identity/v1/identityv1connect"
	"github.com/iFTY-R/game-night/platform/admin/operations"
)

func TestInterceptorBypassesStateLookupForReadsAndAuthenticationRecovery(t *testing.T) {
	reader := &maintenanceReaderStub{err: errors.New("database should not be consulted")}
	interceptor, err := NewInterceptor(reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, procedure := range []string{
		identityv1connect.IdentityServiceGetCurrentIdentityProcedure,
		identityv1connect.IdentityServiceCompleteRecoveryProcedure,
	} {
		t.Run(procedure, func(t *testing.T) {
			called, callErr := callThroughInterceptor(t, interceptor, procedure)
			if callErr != nil || !called {
				t.Fatalf("called = %t, error = %v", called, callErr)
			}
		})
	}
	if reader.calls != 0 {
		t.Fatalf("maintenance reads = %d, want 0", reader.calls)
	}
}

func TestInterceptorConsultsAuthorityForEveryMutation(t *testing.T) {
	reader := &maintenanceReaderStub{state: validMaintenanceState(false)}
	interceptor, err := NewInterceptor(reader)
	if err != nil {
		t.Fatal(err)
	}
	called, callErr := callThroughInterceptor(t, interceptor, identityv1connect.IdentityServiceChangeUsernameProcedure)
	if callErr != nil || !called || reader.calls != 1 {
		t.Fatalf("called = %t, reads = %d, error = %v", called, reader.calls, callErr)
	}
}

func TestInterceptorBlocksKnownAndUnknownMutationsDuringMaintenance(t *testing.T) {
	for _, procedure := range []string{
		identityv1connect.IdentityServiceChangeUsernameProcedure,
		"/unreviewed.v1.Service/NewMutation",
	} {
		t.Run(procedure, func(t *testing.T) {
			reader := &maintenanceReaderStub{state: validMaintenanceState(true)}
			interceptor, err := NewInterceptor(reader)
			if err != nil {
				t.Fatal(err)
			}
			called, callErr := callThroughInterceptor(t, interceptor, procedure)
			if connect.CodeOf(callErr) != connect.CodeUnknown || !strings.Contains(callErr.Error(), ErrMutationBlocked.Error()) || called || reader.calls != 1 {
				t.Fatalf("called = %t, reads = %d, error = %v", called, reader.calls, callErr)
			}
		})
	}
}

func TestInterceptorFailsClosedWhenMaintenanceAuthorityIsUnavailableOrInvalid(t *testing.T) {
	tests := []maintenanceReaderStub{
		{err: operations.ErrRepositoryUnavailable},
		{state: operations.MaintenanceState{}},
	}
	for _, stub := range tests {
		reader := stub
		interceptor, err := NewInterceptor(&reader)
		if err != nil {
			t.Fatal(err)
		}
		called, callErr := callThroughInterceptor(t, interceptor, identityv1connect.IdentityServiceChangeUsernameProcedure)
		if connect.CodeOf(callErr) != connect.CodeUnknown || !strings.Contains(callErr.Error(), ErrStateUnavailable.Error()) || called || reader.calls != 1 {
			t.Fatalf("called = %t, reads = %d, error = %v", called, reader.calls, callErr)
		}
	}
}

type maintenanceReaderStub struct {
	state operations.MaintenanceState
	err   error
	calls int
}

func (reader *maintenanceReaderStub) GetMaintenanceState(context.Context) (operations.MaintenanceState, error) {
	reader.calls++
	return reader.state, reader.err
}

func validMaintenanceState(enabled bool) operations.MaintenanceState {
	return operations.MaintenanceState{Enabled: enabled, Scope: operations.MaintenanceUserMutations, Version: 1}
}

func callThroughInterceptor(t testing.TB, interceptor connect.Interceptor, procedure string) (bool, error) {
	t.Helper()
	called := false
	handler := connect.NewUnaryHandler(
		procedure,
		func(context.Context, *connect.Request[identityv1.GetCurrentIdentityRequest]) (*connect.Response[identityv1.GetCurrentIdentityResponse], error) {
			called = true
			return connect.NewResponse(&identityv1.GetCurrentIdentityResponse{}), nil
		},
		connect.WithInterceptors(interceptor),
	)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := connect.NewClient[identityv1.GetCurrentIdentityRequest, identityv1.GetCurrentIdentityResponse](server.Client(), server.URL+procedure)
	_, err := client.CallUnary(t.Context(), connect.NewRequest(&identityv1.GetCurrentIdentityRequest{}))
	return called, err
}
