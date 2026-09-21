package edgeletapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouter_KnowledgesCollectionIsNotRegistered(t *testing.T) {
	router := NewRouter()
	req := httptest.NewRequest(http.MethodGet, "/v1/knowledges", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected /v1/knowledges to be unregistered (404), got %d body=%s", rec.Code, rec.Body.String())
	}
}
