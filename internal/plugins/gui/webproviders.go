package gui

import (
	"encoding/json"
	"net/http"
)

// The options of the page in a browser, as the window has them: see
// providers.go. What changes the node is sent by the page itself alone, as
// what changes an agent is.

// optionsBody is what the page sends to change the options: which of the
// fields count depends on the request.
type optionsBody struct {
	Scope    string `json:"scope"`
	Access   string `json:"access"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Again    string `json:"again"`
	Answer   string `json:"answer"`
}

// readOptions reads what the page sends into body, and reports whether it
// could; it answers the page itself where it could not.
func (w *webFront) readOptions(rw http.ResponseWriter, r *http.Request, body *optionsBody) bool {
	if !w.sameOrigin(rw, r) {
		return false
	}
	// A key or a token is what is sent at most.
	r.Body = http.MaxBytesReader(rw, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(body); err != nil {
		http.Error(rw, "the request cannot be read: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// answer replies value, or why there is none.
func answer[T any](rw http.ResponseWriter, value T, err error) {
	if err != nil {
		fail(rw, err)
		return
	}
	reply(rw, value)
}

// providers lists the providers and the accesses of the node.
func (w *webFront) providers(rw http.ResponseWriter, _ *http.Request) {
	list, err := w.profile.providerList()
	answer(rw, list, err)
}

// llms tells what the scopes of the node answer with.
func (w *webFront) llms(rw http.ResponseWriter, _ *http.Request) {
	list, err := w.profile.llmList()
	answer(rw, list, err)
}

// setLLM changes what a scope answers with.
func (w *webFront) setLLM(rw http.ResponseWriter, r *http.Request) {
	var body optionsBody
	if !w.readOptions(rw, r, &body) {
		return
	}
	list, err := w.profile.setLLM(body.Scope, body.Access, body.Model)
	answer(rw, list, err)
}

// toggleFallback changes the fallbacks.
func (w *webFront) toggleFallback(rw http.ResponseWriter, r *http.Request) {
	var body optionsBody
	if !w.readOptions(rw, r, &body) {
		return
	}
	list, err := w.profile.toggleFallback(body.Access, body.Model)
	answer(rw, list, err)
}

// setEffort changes the effort.
func (w *webFront) setEffort(rw http.ResponseWriter, r *http.Request) {
	var body optionsBody
	if !w.readOptions(rw, r, &body) {
		return
	}
	list, err := w.profile.setEffort(body.Effort)
	answer(rw, list, err)
}

// renameAccess renames an access.
func (w *webFront) renameAccess(rw http.ResponseWriter, r *http.Request) {
	var body optionsBody
	if !w.readOptions(rw, r, &body) {
		return
	}
	list, err := w.profile.renameAccess(body.ID, body.Name)
	answer(rw, list, err)
}

// removeAccess removes an access.
func (w *webFront) removeAccess(rw http.ResponseWriter, r *http.Request) {
	var body optionsBody
	if !w.readOptions(rw, r, &body) {
		return
	}
	note, err := w.profile.removeAccess(body.ID)
	answer(rw, struct {
		Note string `json:"note"`
	}{note}, err)
}

// startLogin starts a sign-in. A page the provider opens is opened by the
// browser of the page, not on the host of the node.
func (w *webFront) startLogin(rw http.ResponseWriter, r *http.Request) {
	var body optionsBody
	if !w.readOptions(rw, r, &body) {
		return
	}
	id, err := w.profile.startLogin(body.Provider, body.Again, false)
	answer(rw, struct {
		ID string `json:"id"`
	}{id}, err)
}

// loginStep returns the next step of a sign-in, waiting a while for it.
func (w *webFront) loginStep(rw http.ResponseWriter, r *http.Request) {
	var body optionsBody
	if !w.readOptions(rw, r, &body) {
		return
	}
	step, err := w.profile.loginStep(r.Context(), body.ID, loginPoll)
	answer(rw, step, err)
}

// answerLogin answers what a sign-in asks.
func (w *webFront) answerLogin(rw http.ResponseWriter, r *http.Request) {
	var body optionsBody
	if !w.readOptions(rw, r, &body) {
		return
	}
	answer(rw, struct{}{}, w.profile.answerLogin(body.ID, body.Answer))
}

// cancelLogin gives a sign-in up.
func (w *webFront) cancelLogin(rw http.ResponseWriter, r *http.Request) {
	var body optionsBody
	if !w.readOptions(rw, r, &body) {
		return
	}
	answer(rw, struct{}{}, w.profile.cancelLogin(body.ID))
}

// gatewayBody is what the page sends to save a gateway.
type gatewayBody struct {
	Name   string            `json:"name"`
	Change GatewayChangeBody `json:"change"`
}

// gateways lists the gateways of the profile with their settings.
func (w *webFront) gateways(rw http.ResponseWriter, r *http.Request) {
	list, err := w.profile.gatewayList(r.Context())
	answer(rw, list, err)
}

// saveGateway saves what the owner changed of a gateway. The body holds a
// password: it is read here and logged nowhere, and it is not kept.
func (w *webFront) saveGateway(rw http.ResponseWriter, r *http.Request) {
	if !w.sameOrigin(rw, r) {
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, 64<<10)
	var body gatewayBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// Not the error of the decoder: it may quote the body.
		http.Error(rw, "the request cannot be read", http.StatusBadRequest)
		return
	}
	list, err := w.profile.saveGateway(r.Context(), body.Name, body.Change)
	answer(rw, list, err)
}
