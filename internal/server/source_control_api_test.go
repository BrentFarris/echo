package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brent/echo/internal/sourcecontrol"
)

func TestWriteSourceControlErrorMapsFossilUIFailures(t *testing.T) {
	tests := []struct {
		code   string
		status int
	}{
		{code: "fossil_ui_unavailable_in_sandbox", status: http.StatusServiceUnavailable},
		{code: "fossil_ui_start_failed", status: http.StatusUnprocessableEntity},
		{code: "fossil_ui_restart_failed", status: http.StatusUnprocessableEntity},
		{code: "fossil_ui_stop_timeout", status: http.StatusGatewayTimeout},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeSourceControlError(recorder, &sourcecontrol.Error{Code: test.code, Message: "safe message"})
			if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
