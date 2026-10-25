package records

import "testing"

func TestBodyHash_IgnoresAttachmentURLs(t *testing.T) {
	a := `{"id":1,"subject":"Beam","questions":[{"id":10,"attachments":[{"id":100,"name":"p","url":"https://f.example.com/p?sig=a"}],
		"answers":[{"id":11,"attachments":[{"id":101,"url":"https://f.example.com/q?sig=a"}]}]}]}`
	b := `{"subject":"Beam","id":1,"questions":[{"id":10,"attachments":[{"id":100,"name":"p","url":"https://f.example.com/p?sig=b"}],
		"answers":[{"id":11,"attachments":[{"id":101,"url":"https://f.example.com/q?sig=b"}]}]}]}`
	c := `{"id":1,"subject":"Column","questions":[{"id":10,"attachments":[{"id":100,"name":"p","url":"https://f.example.com/p?sig=a"}],
		"answers":[{"id":11,"attachments":[{"id":101,"url":"https://f.example.com/q?sig=a"}]}]}]}`
	if bodyHash([]byte(a)) != bodyHash([]byte(b)) {
		t.Error("bodies differing only in attachment urls hash differently")
	}
	if bodyHash([]byte(a)) == bodyHash([]byte(c)) {
		t.Error("bodies differing in subject hash equal")
	}
	// A url outside an attachments array still counts.
	if bodyHash([]byte(`{"id":1,"link":"https://a"}`)) == bodyHash([]byte(`{"id":1,"link":"https://b"}`)) {
		t.Error("a link change was ignored")
	}
	if bodyHash([]byte(`{"id":1,"url":"https://a"}`)) == bodyHash([]byte(`{"id":1,"url":"https://b"}`)) {
		t.Error("a top-level url change was ignored")
	}
	// An attachment's other fields still count.
	if bodyHash([]byte(`{"attachments":[{"id":1,"name":"a"}]}`)) == bodyHash([]byte(`{"attachments":[{"id":1,"name":"b"}]}`)) {
		t.Error("an attachment name change was ignored")
	}
	// Large ids keep their precision in the canonical form.
	if bodyHash([]byte(`{"id":9007199254740993}`)) == bodyHash([]byte(`{"id":9007199254740992}`)) {
		t.Error("ids beyond float precision hash equal")
	}
	// A body that is not JSON still hashes, by its bytes.
	if bodyHash([]byte(`not json a`)) == bodyHash([]byte(`not json b`)) {
		t.Error("undecodable bodies hash equal")
	}
}
