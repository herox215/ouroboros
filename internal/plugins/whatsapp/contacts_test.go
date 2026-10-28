package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"
)

func TestContactsOfflineRestartSearchAndAliases(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fixture")
	texts, err := openTextStore(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newClient(ctx, path, texts)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*client)
	// Synthetic device only; no real credentials and no network connection.
	dev := c.cli.Store
	self := types.NewJID("999", types.DefaultUserServer)
	dev.ID = &self
	dev.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err = dev.Save(ctx); err != nil {
		t.Fatal(err)
	}
	pn := types.NewJID("123", types.DefaultUserServer)
	lid := types.NewJID("456", types.HiddenUserServer)
	other := types.NewJID("789", types.DefaultUserServer)
	if err = dev.Contacts.PutContactName(ctx, pn, "Kim", "Kim One"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = dev.Contacts.PutPushName(ctx, lid, "KIM Alias"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = dev.Contacts.PutBusinessName(ctx, other, "Kim Two"); err != nil {
		t.Fatal(err)
	}
	if err = dev.LIDs.PutLIDMapping(ctx, lid, pn); err != nil {
		t.Fatal(err)
	}
	if err = texts.put(message{Chat: lid.String(), ID: "incoming", Text: "fixture incoming", Time: 42}); err != nil {
		t.Fatal(err)
	}
	c.Close()
	texts.close()

	s := &service{path: path, makeClient: newClient}
	defer s.close()
	var got []contact
	if err = json.Unmarshal([]byte(call(t, s, "whatsapp_contacts", `{"query":"kIm","limit":"20"}`)), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].FullName != "Kim One" || got[1].PushName != "KIM Alias" || got[2].BusinessName != "Kim Two" {
		t.Fatalf("ambiguous hits: %+v", got)
	}
	if len(got[0].Aliases) != 2 || got[0].Aliases[1] != lid.String() || len(got[1].Aliases) != 2 || got[1].Aliases[1] != pn.String() || len(got[2].Aliases) != 1 {
		t.Fatalf("aliases: %+v", got)
	}
	for _, alias := range got[0].Aliases {
		out := call(t, s, "whatsapp_read", `{"jid":"`+alias+`"}`)
		if alias == lid.String() && !strings.Contains(out, "fixture incoming") {
			t.Fatal(out)
		}
	}
	if s.client.Status().Connected || !s.client.Status().Paired {
		t.Fatal(s.client.Status())
	}
	if _, err = os.Stat(filepath.Join(path, "pairing.png")); !os.IsNotExist(err) {
		t.Fatal("lookup created QR", err)
	}
	if err = json.Unmarshal([]byte(call(t, s, "whatsapp_contacts", `{"query":"kim","limit":"1"}`)), &got); err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	if out := call(t, s, "whatsapp_contacts", `{"query":"absent"}`); out != "[]" {
		t.Fatal(out)
	}
	// Underlying local read failure must surface, never silently return no hits.
	s.client.Close()
	if _, err = tool(t, s, "whatsapp_contacts").Run(ctx, json.RawMessage(`{"query":"kim"}`)); err == nil || strings.Contains(err.Error(), "SELECT") {
		t.Fatal(err)
	}
}

func TestContactsValidationNoConnectAndErrors(t *testing.T) {
	ctx := context.Background()
	f := &fakeBackend{}
	made := 0
	s := &service{path: filepath.Join(t.TempDir(), "fixture"), makeClient: func(context.Context, string, *textStore) (backend, error) { made++; return f, nil }}
	defer s.close()
	for _, args := range []string{`{}`, `{"query":"  "}`, `{"query":"Kim","limit":"0"}`, `{"query":"Kim","limit":"101"}`, `{"query":"Kim","limit":"bad"}`} {
		if _, err := tool(t, s, "whatsapp_contacts").Run(ctx, json.RawMessage(args)); err == nil {
			t.Fatal(args)
		}
	}
	if made != 0 {
		t.Fatal("invalid lookup initialized session")
	}
	call(t, s, "whatsapp_contacts", `{"query":"Kim"}`)
	if made != 1 || f.connects != 0 || f.sends != 0 {
		t.Fatal("lookup connected or sent", f)
	}
	f.err = errors.New("secret local error")
	if _, err := tool(t, s, "whatsapp_contacts").Run(ctx, json.RawMessage(`{"query":"Kim"}`)); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	s.close()
	s = &service{path: filepath.Join(t.TempDir(), "fixture"), makeClient: func(context.Context, string, *textStore) (backend, error) {
		return nil, errors.New("secret initialization")
	}}
	defer s.close()
	if _, err := tool(t, s, "whatsapp_contacts").Run(ctx, json.RawMessage(`{"query":"Kim"}`)); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}

func TestContactsUnpairedNoConnect(t *testing.T) {
	s := &service{path: filepath.Join(t.TempDir(), "fixture"), makeClient: newClient}
	defer s.close()
	if got := call(t, s, "whatsapp_contacts", `{"query":"Kim"}`); got != "[]" {
		t.Fatal(got)
	}
	if st := s.client.Status(); st.Connected || st.Paired || st.QRPath != "" {
		t.Fatal(st)
	}
}

// Aliases that cannot be looked up, or that are no exact JID, fail the
// search: it never answers with less than it knows.
func TestContactsAliasFailures(t *testing.T) {
	for name, sql := range map[string]string{
		"no mappings":    `DROP TABLE whatsmeow_lid_map`,
		"no exact alias": `INSERT INTO whatsmeow_lid_map(lid, pn) VALUES('4a6', '123')`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "fixture")
			if err := privateDir(path); err != nil {
				t.Fatal(err)
			}
			b, err := newClient(ctx, path, nil)
			if err != nil {
				t.Fatal(err)
			}
			dev := b.(*client).cli.Store
			self := types.NewJID("999", types.DefaultUserServer)
			dev.ID = &self
			dev.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
			if err = dev.Save(ctx); err != nil {
				t.Fatal(err)
			}
			if err = dev.Contacts.PutContactName(ctx, types.NewJID("123", types.DefaultUserServer), "Kim", "Kim One"); err != nil {
				t.Fatal(err)
			}
			b.Close()
			db, err := privateDB(filepath.Join(path, "session.db"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(sql); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
			if b, err = newClient(ctx, path, nil); err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if got, err := b.Contacts(ctx, "kim", 10); err == nil {
				t.Fatalf("found %+v", got)
			}
		})
	}
}
