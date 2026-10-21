package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/raduolivas/dt/backend/services/gateway/api"
)

// getMe answers GET /v1/me (operationId getMe): the credential the request
// authenticated with. It lets a partner check a key.
func getMe(w http.ResponseWriter, r *http.Request) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		internalError(w, r) // Mount always authenticates first
		return
	}
	me := api.Me{
		TenantId:    p.TenantID,
		Environment: api.Environment(p.Env),
		Scopes:      make([]api.Scope, 0, len(p.Scopes)),
	}
	me.Credential.Id = p.CredentialID
	me.Credential.Type = api.MeCredentialType(p.Credential)
	for _, s := range p.Scopes {
		me.Scopes = append(me.Scopes, api.Scope(s))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(me)
}
