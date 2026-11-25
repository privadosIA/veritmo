package music

// settings_stub_test.go: PUT /api/settings belongs to the server (it also saves the Ollama address, the browser and
// the motion model). The tests of the engines only need the part that saves the HeartMuLa and DiffRhythm2 addresses,
// tokens and consents, so the harness serves that part with the same rules as the real handler.

import (
	"encoding/json"
	"net/http"
	"strings"

	"veritmo/internal/platform/settings"
	"veritmo/internal/server/api"
)

func (s *testServer) putSettingsStub(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HeartMulaHost     *string `json:"heartmula_host"`
		HeartMulaToken    *string `json:"heartmula_token"`
		HeartMulaAccept   bool    `json:"heartmula_accept"`
		DiffRhythm2Host   *string `json:"diffrhythm2_host"`
		DiffRhythm2Token  *string `json:"diffrhythm2_token"`
		DiffRhythm2Accept bool    `json:"diffrhythm2_accept"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, api.CodeInvalid, err.Error())
		return
	}
	fail := func(status int, err error) {
		api.WriteError(w, status, api.CodeInvalid, err.Error())
	}
	address := func(in *string, validate func(string) (string, error)) (*string, error) {
		if in == nil {
			return nil, nil
		}
		host := ""
		if strings.TrimSpace(*in) != "" {
			addr, err := validate(*in)
			if err != nil {
				return nil, err
			}
			host = addr
		}
		return &host, nil
	}
	token := func(in *string) *string {
		if in == nil {
			return nil
		}
		t := strings.TrimSpace(*in)
		return &t
	}
	hHost, err := address(req.HeartMulaHost, settings.ValidateHeartMulaAddress)
	if err != nil {
		fail(http.StatusBadRequest, err)
		return
	}
	dHost, err := address(req.DiffRhythm2Host, settings.ValidateDiffRhythm2Address)
	if err != nil {
		fail(http.StatusBadRequest, err)
		return
	}
	hTok, dTok := token(req.HeartMulaToken), token(req.DiffRhythm2Token)
	if hHost != nil || hTok != nil {
		host, tok := settings.HeartMula()
		if hHost != nil {
			host = *hHost
		}
		if hTok != nil {
			tok = *hTok
		}
		if err := settings.SetHeartMula(host, tok); err != nil {
			fail(http.StatusInternalServerError, err)
			return
		}
	}
	if req.HeartMulaAccept {
		if err := settings.AcknowledgeHeartMula(); err != nil {
			fail(http.StatusBadRequest, err)
			return
		}
	}
	if dHost != nil || dTok != nil {
		host, tok := settings.DiffRhythm2()
		if dHost != nil {
			host = *dHost
		}
		if dTok != nil {
			tok = *dTok
		}
		if err := settings.SetDiffRhythm2(host, tok); err != nil {
			fail(http.StatusInternalServerError, err)
			return
		}
	}
	if req.DiffRhythm2Accept {
		if err := settings.AcknowledgeDiffRhythm2(); err != nil {
			fail(http.StatusBadRequest, err)
			return
		}
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"heartmula": CurrentHeartMulaSettings(), "diffrhythm2": CurrentDiffRhythm2Settings()})
}
