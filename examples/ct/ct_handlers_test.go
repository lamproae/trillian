package ct

// TODO(Martin2112): Tests that verify the signature on SCTs and STHs. All the signing in here
// uses dummy objects. Real tests might be better done as integration tests on the log operation.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang/glog"
	"github.com/golang/mock/gomock"
	ct "github.com/google/certificate-transparency/go"
	"github.com/google/certificate-transparency/go/fixchain"
	"github.com/google/certificate-transparency/go/x509"
	"github.com/google/trillian"
	"github.com/google/trillian/crypto"
	"github.com/google/trillian/examples/ct/testonly"
	"github.com/google/trillian/util"
	"golang.org/x/net/context"
)

// Arbitrary time for use in tests
var fakeTime = time.Date(2016, 7, 22, 11, 01, 13, 0, time.UTC)

// The deadline should be the above bumped by 500ms
var fakeDeadlineTime = time.Date(2016, 7, 22, 11, 01, 13, 500*1000*1000, time.UTC)
var fakeTimeSource = util.FakeTimeSource{fakeTime}
var okStatus = &trillian.TrillianApiStatus{StatusCode: trillian.TrillianApiStatusCode_OK}

type jsonChain struct {
	Chain []string `json:chain`
}

type getEntriesRangeTestCase struct {
	start          int64
	end            int64
	expectedStatus int
	explanation    string
	rpcExpected    bool
}

var getEntriesRangeTestCases = []getEntriesRangeTestCase{
	{-1, 0, http.StatusBadRequest, "-ve start value not allowed", false},
	{0, -1, http.StatusBadRequest, "-ve end value not allowed", false},
	{20, 10, http.StatusBadRequest, "invalid range end>start", false},
	{3000, -50, http.StatusBadRequest, "invalid range, -ve end", false},
	{10, 20, http.StatusInternalServerError, "valid range", true},
	{10, 10, http.StatusInternalServerError, "valid range, one entry", true},
	{10, 9, http.StatusBadRequest, "invalid range, edge case", false},
	{1000, 50000, http.StatusBadRequest, "range too large to be accepted", false}}

// List of requests for get-entry-and-proof that should be rejected with bad request status
var getEntryAndProofBadRequests = []string{
	"", "leaf_index=b", "leaf_index=1&tree_size=-1", "leaf_index=-1&tree_size=1",
	"leaf_index=1&tree_size=d", "leaf_index=&tree_size=", "leaf_index=", "leaf_index=1&tree_size=0",
	"leaf_index=10&tree_size=5", "leaf_index=tree_size"}

// A list of requests that should result in a bad request status
var getProofByHashBadRequests = []string{"", "hash=&tree_size=1", "hash=''&tree_size=1", "hash=notbase64data&tree_size=1", "tree_size=-1&hash=aGkK"}

// A list of requests for get-sth-consistency that should result in a bad request status
var getSTHConsistencyBadRequests = []string{"", "first=apple&second=orange", "first=1&second=a",
	"first=a&second=2", "first=-1&second=10", "first=10&second=-11", "first=6&second=6",
	"first=998&second=997", "first=1000&second=200", "first=10", "second=20"}

// The result we expect after a roundtrip in the successful get proof by hash test
var expectedInclusionProofByHash = getProofByHashResponse{
	LeafIndex: 2,
	AuditPath: [][]byte{[]byte("abcdef"), []byte("ghijkl"), []byte("mnopqr")}}

// The result we expect after a roundtrip in the successful get sth consistency test
var expectedSTHConsistencyProofByHash = getSTHConsistencyResponse{Consistency: [][]byte{[]byte("abcdef"), []byte("ghijkl"), []byte("mnopqr")}}

const caCertB64 string = `MIIC0DCCAjmgAwIBAgIBADANBgkqhkiG9w0BAQUFADBVMQswCQYDVQQGEwJHQjEk
MCIGA1UEChMbQ2VydGlmaWNhdGUgVHJhbnNwYXJlbmN5IENBMQ4wDAYDVQQIEwVX
YWxlczEQMA4GA1UEBxMHRXJ3IFdlbjAeFw0xMjA2MDEwMDAwMDBaFw0yMjA2MDEw
MDAwMDBaMFUxCzAJBgNVBAYTAkdCMSQwIgYDVQQKExtDZXJ0aWZpY2F0ZSBUcmFu
c3BhcmVuY3kgQ0ExDjAMBgNVBAgTBVdhbGVzMRAwDgYDVQQHEwdFcncgV2VuMIGf
MA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDVimhTYhCicRmTbneDIRgcKkATxtB7
jHbrkVfT0PtLO1FuzsvRyY2RxS90P6tjXVUJnNE6uvMa5UFEJFGnTHgW8iQ8+EjP
KDHM5nugSlojgZ88ujfmJNnDvbKZuDnd/iYx0ss6hPx7srXFL8/BT/9Ab1zURmnL
svfP34b7arnRsQIDAQABo4GvMIGsMB0GA1UdDgQWBBRfnYgNyHPmVNT4DdjmsMEk
tEfDVTB9BgNVHSMEdjB0gBRfnYgNyHPmVNT4DdjmsMEktEfDVaFZpFcwVTELMAkG
A1UEBhMCR0IxJDAiBgNVBAoTG0NlcnRpZmljYXRlIFRyYW5zcGFyZW5jeSBDQTEO
MAwGA1UECBMFV2FsZXMxEDAOBgNVBAcTB0VydyBXZW6CAQAwDAYDVR0TBAUwAwEB
/zANBgkqhkiG9w0BAQUFAAOBgQAGCMxKbWTyIF4UbASydvkrDvqUpdryOvw4BmBt
OZDQoeojPUApV2lGOwRmYef6HReZFSCa6i4Kd1F2QRIn18ADB8dHDmFYT9czQiRy
f1HWkLxHqd81TbD26yWVXeGJPE3VICskovPkQNJ0tU4b03YmnKliibduyqQQkOFP
OwqULg==`

const intermediateCertB64 string = `MIIC3TCCAkagAwIBAgIBCTANBgkqhkiG9w0BAQUFADBVMQswCQYDVQQGEwJHQjEk
MCIGA1UEChMbQ2VydGlmaWNhdGUgVHJhbnNwYXJlbmN5IENBMQ4wDAYDVQQIEwVX
YWxlczEQMA4GA1UEBxMHRXJ3IFdlbjAeFw0xMjA2MDEwMDAwMDBaFw0yMjA2MDEw
MDAwMDBaMGIxCzAJBgNVBAYTAkdCMTEwLwYDVQQKEyhDZXJ0aWZpY2F0ZSBUcmFu
c3BhcmVuY3kgSW50ZXJtZWRpYXRlIENBMQ4wDAYDVQQIEwVXYWxlczEQMA4GA1UE
BxMHRXJ3IFdlbjCBnzANBgkqhkiG9w0BAQEFAAOBjQAwgYkCgYEA12pnjRFvUi5V
/4IckGQlCLcHSxTXcRWQZPeSfv3tuHE1oTZe594Yy9XOhl+GDHj0M7TQ09NAdwLn
o+9UKx3+m7qnzflNxZdfxyn4bxBfOBskNTXPnIAPXKeAwdPIRADuZdFu6c9S24rf
/lD1xJM1CyGQv1DVvDbzysWo2q6SzYsCAwEAAaOBrzCBrDAdBgNVHQ4EFgQUllUI
BQJ4R56Hc3ZBMbwUOkfiKaswfQYDVR0jBHYwdIAUX52IDchz5lTU+A3Y5rDBJLRH
w1WhWaRXMFUxCzAJBgNVBAYTAkdCMSQwIgYDVQQKExtDZXJ0aWZpY2F0ZSBUcmFu
c3BhcmVuY3kgQ0ExDjAMBgNVBAgTBVdhbGVzMRAwDgYDVQQHEwdFcncgV2VuggEA
MAwGA1UdEwQFMAMBAf8wDQYJKoZIhvcNAQEFBQADgYEAIgbascZrcdzglcP2qi73
LPd2G+er1/w5wxpM/hvZbWc0yoLyLd5aDIu73YJde28+dhKtjbMAp+IRaYhgIyYi
hMOqXSGR79oQv5I103s6KjQNWUGblKSFZvP6w82LU9Wk6YJw6tKXsHIQ+c5KITix
iBEUO5P6TnqH3TfhOF8sKQg=`

const caAndIntermediateCertsPEM string = "-----BEGIN CERTIFICATE-----\n" + caCertB64 + "\n-----END CERTIFICATE-----\n" +
	"\n-----BEGIN CERTIFICATE-----\n" + intermediateCertB64 + "\n-----END CERTIFICATE-----\n"

// Used in test of corrupt merkle leaves
const invalidLeafString string = "NOT A MERKLE TREE LEAF"

type handlerAndPath struct {
	path    string
	handler appHandler
}

func allGetHandlersForTest(trustedRoots *PEMCertPool, c CTRequestHandlers) []handlerAndPath {
	return []handlerAndPath{
		{"get-sth", wrappedGetSTHHandler(c)},
		{"get-sth-consistency", wrappedGetSTHConsistencyHandler(c)},
		{"get-proof-by-hash", wrappedGetProofByHashHandler(c)},
		{"get-entries", wrappedGetEntriesHandler(c)},
		{"get-roots", wrappedGetRootsHandler(trustedRoots)},
		{"get-entry-and-proof", wrappedGetEntryAndProofHandler(c)}}
}

func allPostHandlersForTest(client trillian.TrillianLogClient) []handlerAndPath {
	pool := NewPEMCertPool()
	ok := pool.AppendCertsFromPEM([]byte(testonly.FakeCACertPem))

	if !ok {
		glog.Fatal("Failed to load cert pool")
	}

	return []handlerAndPath{
		{"add-chain", wrappedAddChainHandler(CTRequestHandlers{rpcClient: client, trustedRoots: pool})},
		{"add-pre-chain", wrappedAddPreChainHandler(CTRequestHandlers{rpcClient: client, trustedRoots: pool})}}
}

func TestPostHandlersOnlyAcceptPost(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)

	// Anything in the post handler list should only accept POST
	for _, hp := range allPostHandlersForTest(client) {
		s := httptest.NewServer(hp.handler)
		defer s.Close()
		resp, err := http.Get(s.URL + "/ct/v1/" + hp.path)

		if err != nil {
			t.Fatal(err)
		}

		// TODO(Martin2112): Remove this test when there are no more handlers to be implemented and
		// rely on the handlers own tests
		if expected, got := http.StatusMethodNotAllowed, resp.StatusCode; expected != got {
			t.Fatalf("Wrong status code for GET to POST handler, expected %v got %v", expected, got)
		}

		resp, err = http.Post(s.URL+"/ct/v1/"+hp.path, "application/json", nil)

		if err != nil {
			t.Fatal(err)
		}

		if expected, got := http.StatusBadRequest, resp.StatusCode; expected != got {
			t.Fatalf("Wrong status code for POST to POST handler, expected %v got %v", expected, got)
		}
	}
}

func TestGetHandlersRejectPost(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	pool := NewPEMCertPool()
	handlers := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource}

	// Anything in the get handler list should not accept POST. We don't test they accept
	// GET because that needs different mock backend set up per handler.
	for _, hp := range allGetHandlersForTest(pool, handlers) {
		s := httptest.NewServer(hp.handler)
		defer s.Close()

		resp, err := http.Post(s.URL+"/ct/v1/"+hp.path, "application/json", nil)

		if err != nil {
			t.Fatal(err)
		}

		if expected, got := http.StatusMethodNotAllowed, resp.StatusCode; expected != got {
			t.Fatalf("Wrong status code for POST to GET handler, expected %v, got %v", expected, got)
		}
	}
}

func TestPostHandlersRejectEmptyJson(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)

	for _, hp := range allPostHandlersForTest(client) {
		s := httptest.NewServer(hp.handler)
		defer s.Close()

		resp, err := http.Post(s.URL+"/ct/v1/"+hp.path, "application/json", strings.NewReader(""))

		if err != nil {
			t.Fatal(err)
		}

		if expected, got := http.StatusBadRequest, resp.StatusCode; expected != got {
			t.Fatalf("Wrong status code for empty JSON body, expected %v, got %v", expected, got)
		}
	}
}

func TestPostHandlersRejectMalformedJson(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)

	for _, hp := range allPostHandlersForTest(client) {
		s := httptest.NewServer(hp.handler)
		defer s.Close()

		resp, err := http.Post(s.URL+"/ct/v1/"+hp.path, "application/json", strings.NewReader("{ !£$%^& not valid json "))

		if err != nil {
			t.Fatal(err)
		}

		if expected, got := http.StatusBadRequest, resp.StatusCode; expected != got {
			t.Fatalf("Wrong status code for invalid JSON body, expected %v, got %v", expected, got)
		}
	}
}

func TestPostHandlersRejectEmptyCertChain(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)

	for _, hp := range allPostHandlersForTest(client) {
		s := httptest.NewServer(hp.handler)
		defer s.Close()

		resp, err := http.Post(s.URL+"/ct/v1/"+hp.path, "application/json", strings.NewReader(`{ "chain": [] }`))

		if err != nil {
			t.Fatal(err)
		}

		if expected, got := http.StatusBadRequest, resp.StatusCode; expected != got {
			t.Fatalf("Wrong status code for empty chain in JSON body, expected %v, got %v", expected, got)
		}
	}
}

func TestPostHandlersAcceptNonEmptyCertChain(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)

	for _, hp := range allPostHandlersForTest(client) {
		s := httptest.NewServer(hp.handler)
		defer s.Close()

		resp, err := http.Post(s.URL+"/ct/v1/"+hp.path, "application/json", strings.NewReader(`{ "chain": [ "test" ] }`))

		if err != nil {
			t.Fatal(err)
		}

		// TODO(Martin2112): Remove not implemented from test when all the handlers have been written
		// For now they return not implemented as the handler is a stub
		if expected1, expected2, got := http.StatusNotImplemented, http.StatusBadRequest, resp.StatusCode; expected1 != got && expected2 != got {
			t.Fatalf("Wrong status code for non-empty chain in body, expected either %v or %v, got %v", expected1, expected2, got)
		}
	}
}

func TestGetRoots(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	roots := loadCertsIntoPoolOrDie(t, []string{caAndIntermediateCertsPEM})
	handler := wrappedGetRootsHandler(roots)

	req, err := http.NewRequest("GET", "http://example.com/ct/v1/get-roots", nil)
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if expected, got := http.StatusOK, w.Code; expected != got {
		t.Fatalf("Wrong status code for get-roots, expected %v, got %v", expected, got)
	}

	var parsedJson map[string][]string
	if err := json.Unmarshal(w.Body.Bytes(), &parsedJson); err != nil {
		t.Fatalf("Failed to unmarshal json response: %s", w.Body.Bytes())
	}
	if expected, got := 1, len(parsedJson); expected != got {
		t.Fatalf("Expected %v entry(s) in json map, got %v", expected, got)
	}
	certs := parsedJson[jsonMapKeyCertificates]
	if expected, got := 2, len(certs); expected != got {
		t.Fatalf("Expected %v root certs got %v: %v", expected, got, certs)
	}
	if expected, got := strings.Replace(caCertB64, "\n", "", -1), certs[0]; expected != got {
		t.Fatalf("First root cert mismatched, expected %s got %s", expected, got)
	}
	if expected, got := strings.Replace(intermediateCertB64, "\n", "", -1), certs[1]; expected != got {
		t.Fatalf("Second root cert mismatched, expected %s got %s", expected, got)
	}
}

// This uses the fake CA as trusted root and submits a chain of just a leaf which should be rejected
// because there's no complete path to the root
func TestAddChainMissingIntermediate(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	km := crypto.NewMockKeyManager(mockCtrl)

	roots := loadCertsIntoPoolOrDie(t, []string{testonly.FakeCACertPem})
	reqHandlers := CTRequestHandlers{0x42, roots, client, km, time.Millisecond * 500, fakeTimeSource}

	pool := loadCertsIntoPoolOrDie(t, []string{testonly.LeafSignedByFakeIntermediateCertPem})
	chain := createJsonChain(t, *pool)

	recorder := makeAddChainRequest(t, reqHandlers, chain)

	if got, want := recorder.Code, http.StatusBadRequest; got != want {
		t.Fatalf("Expected %v for incomplete add-chain got %v. Body: %v", want, got, recorder.Body)
	}
}

// This uses a fake CA as trusted root and submits a chain of just a precert leaf which should be
// rejected
func TestAddChainPrecert(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	km := crypto.NewMockKeyManager(mockCtrl)

	roots := loadCertsIntoPoolOrDie(t, []string{testonly.CACertPEM})
	reqHandlers := CTRequestHandlers{0x42, roots, client, km, time.Millisecond * 500, fakeTimeSource}

	// TODO(Martin2112): I don't think CT should return NonFatalError for something we expect
	// to happen - seeing a precert extension. If this is fixed upstream remove all references from
	// our tests.
	precert, err := fixchain.CertificateFromPEM(testonly.PrecertPEMValid)

	if _, ok := err.(x509.NonFatalErrors); err != nil && !ok {
		t.Fatalf("Unexpected error loading certificate: %v", err)
	}
	pool := NewPEMCertPool()
	pool.AddCert(precert)
	chain := createJsonChain(t, *pool)

	recorder := makeAddChainRequest(t, reqHandlers, chain)

	if got, want := recorder.Code, http.StatusBadRequest; got != want {
		t.Fatalf("expected %v for precert add-chain, got %v. Body: %v", want, got, recorder.Body)
	}
}

// This uses the fake CA as trusted root and submits a chain leaf -> fake intermediate, the
// backend RPC fails so we get a 500
func TestAddChainRPCFails(t *testing.T) {
	toSign := []byte{0x7a, 0xc4, 0xd9, 0xca, 0x5f, 0x2e, 0x23, 0x82, 0xfe, 0xef, 0x5e, 0x95, 0x64, 0x7b, 0x31, 0x11, 0xf, 0x2a, 0x9b, 0x78, 0xa8, 0x3, 0x30, 0x8d, 0xfc, 0x8b, 0x78, 0x6, 0x61, 0xe7, 0x58, 0x44}
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	km := setupMockKeyManager(mockCtrl, toSign)

	roots := loadCertsIntoPoolOrDie(t, []string{testonly.FakeCACertPem})
	reqHandlers := CTRequestHandlers{0x42, roots, client, km, time.Millisecond * 500, fakeTimeSource}

	pool := loadCertsIntoPoolOrDie(t, []string{testonly.LeafSignedByFakeIntermediateCertPem, testonly.FakeIntermediateCertPem})
	chain := createJsonChain(t, *pool)

	// Ignore returned SCT. That's sent to the client and we're testing frontend -> backend interaction
	merkleLeaf, _, err := signV1SCTForCertificate(km, pool.RawCertificates()[0], fakeTime)

	if err != nil {
		t.Fatal(err)
	}

	leaves := leafProtosForCert(t, km, pool.RawCertificates(), merkleLeaf)

	client.EXPECT().QueueLeaves(deadlineMatcher(), &trillian.QueueLeavesRequest{LogId: 0x42, Leaves: leaves}).Return(&trillian.QueueLeavesResponse{Status: &trillian.TrillianApiStatus{StatusCode: trillian.TrillianApiStatusCode(trillian.TrillianApiStatusCode_ERROR)}}, nil)

	recorder := makeAddChainRequest(t, reqHandlers, chain)

	if got, want := recorder.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("expected %v for backend rpc fail on add-chain, got %v. Body: %v", want, got, recorder.Body)
	}
}

// This uses the fake CA as trusted root and submits a chain leaf -> fake intermediate, which
// should be accepted
func TestAddChain(t *testing.T) {
	toSign := []byte{0x7a, 0xc4, 0xd9, 0xca, 0x5f, 0x2e, 0x23, 0x82, 0xfe, 0xef, 0x5e, 0x95, 0x64, 0x7b, 0x31, 0x11, 0xf, 0x2a, 0x9b, 0x78, 0xa8, 0x3, 0x30, 0x8d, 0xfc, 0x8b, 0x78, 0x6, 0x61, 0xe7, 0x58, 0x44}
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	km := setupMockKeyManager(mockCtrl, toSign)

	roots := loadCertsIntoPoolOrDie(t, []string{testonly.FakeCACertPem})
	reqHandlers := CTRequestHandlers{0x42, roots, client, km, time.Millisecond * 500, fakeTimeSource}

	pool := loadCertsIntoPoolOrDie(t, []string{testonly.LeafSignedByFakeIntermediateCertPem, testonly.FakeIntermediateCertPem})
	chain := createJsonChain(t, *pool)

	// Ignore returned SCT. That's sent to the client and we're testing frontend -> backend interaction
	merkleLeaf, _, err := signV1SCTForCertificate(km, pool.RawCertificates()[0], fakeTime)

	if err != nil {
		t.Fatal(err)
	}

	leaves := leafProtosForCert(t, km, pool.RawCertificates(), merkleLeaf)

	client.EXPECT().QueueLeaves(deadlineMatcher(), &trillian.QueueLeavesRequest{LogId: 0x42, Leaves: leaves}).Return(&trillian.QueueLeavesResponse{Status: &trillian.TrillianApiStatus{StatusCode: trillian.TrillianApiStatusCode_OK}}, nil)

	recorder := makeAddChainRequest(t, reqHandlers, chain)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("expected %v for valid add-chain, got %v. Body: %v", want, got, recorder.Body)
	}

	// Roundtrip the response and make sure it's sensible
	var resp addChainResponse
	if err = json.NewDecoder(recorder.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to unmarshal json: %v, body: %v", err, recorder.Body.Bytes())
	}

	if got, want := ct.Version(resp.SctVersion), ct.V1; got != want {
		t.Fatalf("Got SctVersion %v, expected %v", got, want)
	}
	if got, want := resp.ID, ctMockLogID; got != want {
		t.Fatalf("Got logID %s, expected %s", got, want)
	}
	if got, want := resp.Timestamp, uint64(1469185273000000); got != want {
		t.Fatalf("Got timestamp %d, expected %d", got, want)
	}
	if got, want := resp.Signature, "BAEABnNpZ25lZA=="; got != want {
		t.Fatalf("Got signature %s, expected %s", got, want)
	}
}

// Submit a chain with a valid precert but not signed by next cert in chain. Should be rejected.
func TestAddPrecertChainInvalidPath(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	km := crypto.NewMockKeyManager(mockCtrl)

	roots := loadCertsIntoPoolOrDie(t, []string{testonly.CACertPEM})
	reqHandlers := CTRequestHandlers{0x42, roots, client, km, time.Millisecond * 500, fakeTimeSource}

	cert, err := fixchain.CertificateFromPEM(testonly.PrecertPEMValid)
	_, ok := err.(x509.NonFatalErrors)

	if err != nil && !ok {
		t.Fatal(err)
	}

	pool := NewPEMCertPool()
	pool.AddCert(cert)
	// This isn't a valid chain, the intermediate didn't sign the leaf
	cert, err = fixchain.CertificateFromPEM(testonly.FakeIntermediateCertPem)

	if err != nil {
		t.Fatal(err)
	}

	pool.AddCert(cert)

	chain := createJsonChain(t, *pool)

	recorder := makeAddPrechainRequest(t, reqHandlers, chain)

	if got, want := recorder.Code, http.StatusBadRequest; got != want {
		t.Fatalf("expected %v for invaid add-precert-chain, got %v. Body: %v", want, got, recorder.Body)
	}
}

// Submit a chain as precert with a valid path but using a cert instead of a precert. Should be rejected.
func TestAddPrecertChainCert(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	km := crypto.NewMockKeyManager(mockCtrl)

	roots := loadCertsIntoPoolOrDie(t, []string{testonly.CACertPEM})
	reqHandlers := CTRequestHandlers{0x42, roots, client, km, time.Millisecond * 500, fakeTimeSource}

	cert, err := fixchain.CertificateFromPEM(testonly.TestCertPEM)

	if err != nil {
		t.Fatal(err)
	}

	pool := NewPEMCertPool()
	pool.AddCert(cert)
	chain := createJsonChain(t, *pool)

	recorder := makeAddPrechainRequest(t, reqHandlers, chain)

	if got, want := recorder.Code, http.StatusBadRequest; got != want {
		t.Fatalf("expected %v for cert add-precert-chain, got %v. Body: %v", want, got, recorder.Body)
	}
}

// Submit a chain that should be OK but arrange for the backend RPC to fail. Failure should
// be propagated.
func TestAddPrecertChainRPCFails(t *testing.T) {
	toSign := []byte{0xe4, 0x58, 0xf3, 0x6f, 0xbd, 0xed, 0x2e, 0x62, 0x53, 0x30, 0xb3, 0x4, 0x73, 0x10, 0xb4, 0xe2, 0xe1, 0xa7, 0x44, 0x9e, 0x1f, 0x16, 0x6f, 0x78, 0x61, 0x98, 0x32, 0xe5, 0x43, 0x5a, 0x21, 0xff}
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	km := setupMockKeyManager(mockCtrl, toSign)

	roots := loadCertsIntoPoolOrDie(t, []string{testonly.CACertPEM})
	reqHandlers := CTRequestHandlers{0x42, roots, client, km, time.Millisecond * 500, fakeTimeSource}

	cert, err := fixchain.CertificateFromPEM(testonly.PrecertPEMValid)
	_, ok := err.(x509.NonFatalErrors)

	if err != nil && !ok {
		t.Fatal(err)
	}

	pool := NewPEMCertPool()
	pool.AddCert(cert)
	chain := createJsonChain(t, *pool)

	// Ignore returned SCT. That's sent to the client and we're testing frontend -> backend interaction
	merkleLeaf, _, err := signV1SCTForPrecertificate(km, pool.RawCertificates()[0], fakeTime)

	if err != nil {
		t.Fatal(err)
	}

	leaves := leafProtosForCert(t, km, pool.RawCertificates(), merkleLeaf)

	client.EXPECT().QueueLeaves(deadlineMatcher(), &trillian.QueueLeavesRequest{LogId: 0x42, Leaves: leaves}).Return(&trillian.QueueLeavesResponse{Status: &trillian.TrillianApiStatus{StatusCode: trillian.TrillianApiStatusCode(trillian.TrillianApiStatusCode_ERROR)}}, nil)

	recorder := makeAddPrechainRequest(t, reqHandlers, chain)

	if got, want := recorder.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("expected %v for backend rpc fail on add-chain, got %v. Body: %v", want, got, recorder.Body)
	}
}

// Submit a chain with a valid precert signed by a trusted root. Should be accepted.
func TestAddPrecertChain(t *testing.T) {
	toSign := []byte{0xe4, 0x58, 0xf3, 0x6f, 0xbd, 0xed, 0x2e, 0x62, 0x53, 0x30, 0xb3, 0x4, 0x73, 0x10, 0xb4, 0xe2, 0xe1, 0xa7, 0x44, 0x9e, 0x1f, 0x16, 0x6f, 0x78, 0x61, 0x98, 0x32, 0xe5, 0x43, 0x5a, 0x21, 0xff}
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	km := setupMockKeyManager(mockCtrl, toSign)

	roots := loadCertsIntoPoolOrDie(t, []string{testonly.CACertPEM})
	reqHandlers := CTRequestHandlers{0x42, roots, client, km, time.Millisecond * 500, fakeTimeSource}

	cert, err := fixchain.CertificateFromPEM(testonly.PrecertPEMValid)
	_, ok := err.(x509.NonFatalErrors)

	if err != nil && !ok {
		t.Fatal(err)
	}

	pool := NewPEMCertPool()
	pool.AddCert(cert)
	chain := createJsonChain(t, *pool)

	// Ignore returned SCT. That's sent to the client and we're testing frontend -> backend interaction
	merkleLeaf, _, err := signV1SCTForPrecertificate(km, pool.RawCertificates()[0], fakeTime)

	if err != nil {
		t.Fatal(err)
	}

	leaves := leafProtosForCert(t, km, pool.RawCertificates(), merkleLeaf)

	client.EXPECT().QueueLeaves(deadlineMatcher(), &trillian.QueueLeavesRequest{LogId: 0x42, Leaves: leaves}).Return(&trillian.QueueLeavesResponse{Status: &trillian.TrillianApiStatus{StatusCode: trillian.TrillianApiStatusCode_OK}}, nil)

	recorder := makeAddPrechainRequest(t, reqHandlers, chain)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("expected %v for valid add-pre-chain, got %v. Body: %v", want, got, recorder.Body)
	}

	// Roundtrip the response and make sure it's sensible
	var resp addChainResponse
	if err = json.NewDecoder(recorder.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to unmarshal json: %v, body: %v", err, recorder.Body.Bytes())
	}

	if got, want := ct.Version(resp.SctVersion), ct.V1; got != want {
		t.Fatalf("Got SctVersion %v, expected %v", got, want)
	}
	if got, want := resp.ID, ctMockLogID; got != want {
		t.Fatalf("Got logID %s, expected %s", got, want)
	}
	if got, want := resp.Timestamp, uint64(1469185273000000); got != want {
		t.Fatalf("Got timestamp %d, expected %d", got, want)
	}
	if got, want := resp.Signature, "BAEABnNpZ25lZA=="; got != want {
		t.Fatalf("Got signature %s, expected %s", got, want)
	}
}

func TestGetSTHBackendErrorFails(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	km := crypto.NewMockKeyManager(mockCtrl)

	roots := loadCertsIntoPoolOrDie(t, []string{testonly.CACertPEM})
	client.EXPECT().GetLatestSignedLogRoot(deadlineMatcher(), &trillian.GetLatestSignedLogRootRequest{LogId: 0x42}).Return(nil, errors.New("backendfailure"))
	reqHandlers := CTRequestHandlers{0x42, roots, client, km, time.Millisecond * 500, fakeTimeSource}
	handler := wrappedGetSTHHandler(reqHandlers)

	req, err := http.NewRequest("GET", "http://example.com/ct/v1/get-sth", nil)
	if err != nil {
		t.Fatalf("get-sth test request setup failed: %v", err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("Expected %v, got %v", want, got)
	}
	if want, in := "rpc failed", w.Body.String(); !strings.Contains(in, want) {
		t.Fatalf("Expected to find %s within %s", want, in)
	}
}

func TestGetSTHInvalidBackendTreeSizeFails(t *testing.T) {
	// This tests that if the backend returns an impossible tree size it doesn't get sent
	// to the client
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	km := crypto.NewMockKeyManager(mockCtrl)

	roots := loadCertsIntoPoolOrDie(t, []string{testonly.CACertPEM})
	client.EXPECT().GetLatestSignedLogRoot(deadlineMatcher(), &trillian.GetLatestSignedLogRootRequest{LogId: 0x42}).Return(makeGetRootResponseForTest(12345, -50, []byte("abcdabcdabcdabcdabcdabcdabcdabcd")), nil)
	reqHandlers := CTRequestHandlers{0x42, roots, client, km, time.Millisecond * 500, fakeTimeSource}
	handler := wrappedGetSTHHandler(reqHandlers)

	req, err := http.NewRequest("GET", "http://example.com/ct/v1/get-sth", nil)
	if err != nil {
		t.Fatalf("get-sth test request setup failed: %v", err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("Got %v expected %v", got, want)
	}
	if want, in := "bad tree size", w.Body.String(); !strings.Contains(in, want) {
		t.Fatalf("Expected to find %s within %s", want, in)
	}
}

func TestGetSTHMissingRootHashFails(t *testing.T) {
	// This tests that if the backend returns a corrupt hash it doesn't get sent to the client
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	km := crypto.NewMockKeyManager(mockCtrl)

	roots := loadCertsIntoPoolOrDie(t, []string{testonly.CACertPEM})
	client.EXPECT().GetLatestSignedLogRoot(deadlineMatcher(), &trillian.GetLatestSignedLogRootRequest{LogId: 0x42}).Return(makeGetRootResponseForTest(12345, 25, []byte("thisisnot32byteslong")), nil)
	reqHandlers := CTRequestHandlers{0x42, roots, client, km, time.Millisecond * 500, fakeTimeSource}
	handler := wrappedGetSTHHandler(reqHandlers)

	req, err := http.NewRequest("GET", "http://example.com/ct/v1/get-sth", nil)
	if err != nil {
		t.Fatalf("get-sth test request setup failed: %v", err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("Got %v expected %v", got, want)
	}
	if want, in := "bad hash size", w.Body.String(); !strings.Contains(in, want) {
		t.Fatalf("Expected to find %s within %s", want, in)
	}
}

func TestGetSTHSigningFails(t *testing.T) {
	// Arranges for the signing to fail, ensures we do the right thing
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	km := crypto.NewMockKeyManager(mockCtrl)

	signer := crypto.NewMockSigner(mockCtrl)
	signer.EXPECT().Sign(gomock.Any(), gomock.Any(), gomock.Any()).Return([]byte{}, errors.New("signerfails"))
	km.EXPECT().Signer().Return(signer, nil)

	roots := loadCertsIntoPoolOrDie(t, []string{testonly.CACertPEM})
	client.EXPECT().GetLatestSignedLogRoot(deadlineMatcher(), &trillian.GetLatestSignedLogRootRequest{LogId: 0x42}).Return(makeGetRootResponseForTest(12345, 25, []byte("abcdabcdabcdabcdabcdabcdabcdabcd")), nil)
	reqHandlers := CTRequestHandlers{0x42, roots, client, km, time.Millisecond * 500, fakeTimeSource}
	handler := wrappedGetSTHHandler(reqHandlers)

	req, err := http.NewRequest("GET", "http://example.com/ct/v1/get-sth", nil)
	if err != nil {
		t.Fatalf("get-sth test request setup failed: %v", err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("Got %v expected %v", got, want)
	}
	if want, in := "signerfails", w.Body.String(); !strings.Contains(in, want) {
		t.Fatalf("Expected to find %s within %s", want, in)
	}
}

func TestGetSTH(t *testing.T) {
	toSign := []byte{0x1e, 0x88, 0x54, 0x6f, 0x51, 0x57, 0xbf, 0xaf, 0x77, 0xca, 0x24, 0x54, 0x69, 0xb, 0x60, 0x26, 0x31, 0xfe, 0xda, 0xe9, 0x25, 0xbb, 0xe7, 0xcf, 0x70, 0x8e, 0xa2, 0x75, 0x97, 0x5b, 0xfe, 0x74}
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	km := setupMockKeyManagerForSth(mockCtrl, toSign)

	roots := loadCertsIntoPoolOrDie(t, []string{testonly.CACertPEM})
	client.EXPECT().GetLatestSignedLogRoot(deadlineMatcher(), &trillian.GetLatestSignedLogRootRequest{LogId: 0x42}).Return(makeGetRootResponseForTest(12345000000, 25, []byte("abcdabcdabcdabcdabcdabcdabcdabcd")), nil)
	reqHandlers := CTRequestHandlers{0x42, roots, client, km, time.Millisecond * 500, fakeTimeSource}
	handler := wrappedGetSTHHandler(reqHandlers)

	req, err := http.NewRequest("GET", "http://example.com/ct/v1/get-sth", nil)
	if err != nil {
		t.Fatalf("get-sth test request setup failed: %v", err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusOK; got != want {
		t.Fatalf("Got %v expected %v", got, want)
	}

	// Now roundtrip the response and check we got the expected data
	var parsedJson getSTHResponse
	if err := json.Unmarshal(w.Body.Bytes(), &parsedJson); err != nil {
		t.Fatalf("Failed to unmarshal json response: %s", w.Body.Bytes())
	}

	if got, want := parsedJson.TreeSize, int64(25); got != want {
		t.Fatalf("Got treesize %d, expected %d", got, want)
	}
	if got, want := parsedJson.TimestampMillis, int64(12345); got != want {
		t.Fatalf("Got timestamp %d, expected %d", got, want)
	}
	if got, want := base64.StdEncoding.EncodeToString(parsedJson.RootHash), "YWJjZGFiY2RhYmNkYWJjZGFiY2RhYmNkYWJjZGFiY2Q="; got != want {
		t.Fatalf("Got roothash %s, expected %s", got, want)
	}
	if got, want := base64.StdEncoding.EncodeToString(parsedJson.Signature), "c2lnbmVk"; got != want {
		t.Fatalf("Got signature %s, expected %s", got, want)
	}
}

func loadCertsIntoPoolOrDie(t *testing.T, certs []string) *PEMCertPool {
	pool := NewPEMCertPool()

	for _, cert := range certs {
		ok := pool.AppendCertsFromPEM([]byte(cert))

		if !ok {
			t.Fatalf("couldn't parse test certs: %v", certs)
		}
	}

	return pool
}

func TestGetEntriesRejectsNonNumericParams(t *testing.T) {
	getEntriesTestHelper(t, "start=&&&&&&&&&end=wibble", http.StatusBadRequest, "invalid &&s")
	getEntriesTestHelper(t, "start=fish&end=3", http.StatusBadRequest, "start non numeric")
	getEntriesTestHelper(t, "start=10&end=wibble", http.StatusBadRequest, "end non numeric")
	getEntriesTestHelper(t, "start=fish&end=wibble", http.StatusBadRequest, "both non numeric")
}

func TestGetEntriesRejectsMissingParams(t *testing.T) {
	getEntriesTestHelper(t, "start=1", http.StatusBadRequest, "end missing")
	getEntriesTestHelper(t, "end=1", http.StatusBadRequest, "start missing")
	getEntriesTestHelper(t, "", http.StatusBadRequest, "both missing")
}

func TestGetEntriesRanges(t *testing.T) {
	// This tests that only valid ranges make it to the backend for get-entries.
	// We're testing request handling up to the point where we make the RPC so arrange for
	// it to fail with a specific error.
	for _, testCase := range getEntriesRangeTestCases {
		mockCtrl := gomock.NewController(t)

		client := trillian.NewMockTrillianLogClient(mockCtrl)

		if testCase.rpcExpected {
			client.EXPECT().GetLeavesByIndex(deadlineMatcher(), &trillian.GetLeavesByIndexRequest{LeafIndex: buildIndicesForRange(testCase.start, testCase.end)}).Return(nil, errors.New("RPCMADE"))
		}

		c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
		handler := wrappedGetEntriesHandler(c)

		path := fmt.Sprintf("/ct/v1/get-entries?start=%d&end=%d", testCase.start, testCase.end)
		req, err := http.NewRequest("GET", path, nil)

		if err != nil {
			t.Fatal(err)
		}

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if expected, got := testCase.expectedStatus, w.Code; expected != got {
			t.Fatalf("expected status %d, got %d for test case %s", expected, got, testCase.explanation)
		}

		// Additionally check that we saw our expected backend error and didn't get the result by
		// chance
		if testCase.expectedStatus == http.StatusInternalServerError {
			if !strings.Contains(w.Body.String(), "RPCMADE") {
				t.Fatalf("Did not get expected backend error: %s\n%s", testCase.explanation, w.Body)
			}
		}
		mockCtrl.Finish()
	}
}

func TestGetEntriesErrorFromBackend(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)

	client.EXPECT().GetLeavesByIndex(deadlineMatcher(), &trillian.GetLeavesByIndexRequest{LeafIndex: []int64{1, 2}}).Return(nil, errors.New("Bang!"))

	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetEntriesHandler(c)

	req, err := http.NewRequest("GET", "/ct/v1/get-entries?start=1&end=2", nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("Expected %v for backend error, got %v. Body: %v", want, got, w.Body)
	}
	if want, in := "Bang!", w.Body.String(); !strings.Contains(in, want) {
		t.Fatalf("Unexpected error: %v", in)
	}
}

func TestGetEntriesBackendReturnedExtraLeaves(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)

	rpcLeaves := []*trillian.LeafProto{{LeafIndex: 1}, {LeafIndex: 2}, {LeafIndex: 3}}
	client.EXPECT().GetLeavesByIndex(deadlineMatcher(), &trillian.GetLeavesByIndexRequest{LeafIndex: []int64{1, 2}}).Return(&trillian.GetLeavesByIndexResponse{Status: okStatus, Leaves: rpcLeaves}, nil)

	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetEntriesHandler(c)

	req, err := http.NewRequest("GET", "/ct/v1/get-entries?start=1&end=2", nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("expected %v for backend too many leaves, got %v. Body: %v", want, got, w.Body)
	}
	if in, want := w.Body.String(), "too many leaves"; !strings.Contains(in, want) {
		t.Fatalf("unexpected error for too many leaves %s", in)
	}
}

func TestGetEntriesBackendReturnedNonContiguousRange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)

	rpcLeaves := []*trillian.LeafProto{{LeafIndex: 1}, {LeafIndex: 3}}
	client.EXPECT().GetLeavesByIndex(deadlineMatcher(), &trillian.GetLeavesByIndexRequest{LeafIndex: []int64{1, 2}}).Return(&trillian.GetLeavesByIndexResponse{Status: okStatus, Leaves: rpcLeaves}, nil)

	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetEntriesHandler(c)

	req, err := http.NewRequest("GET", "/ct/v1/get-entries?start=1&end=2", nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("expected %v for backend too many leaves, got %v. Body: %v", want, got, w.Body)
	}
	if in, want := w.Body.String(), "non contiguous"; !strings.Contains(in, want) {
		t.Fatalf("unexpected error for invalid sparse range: %s", in)
	}
}

func TestGetEntriesLeafCorrupt(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)

	rpcLeaves := []*trillian.LeafProto{{LeafIndex: 1, LeafHash: []byte("hash"), LeafData: []byte(invalidLeafString)}, {LeafIndex: 2, LeafHash: []byte("hash"), LeafData: []byte(invalidLeafString)}}
	client.EXPECT().GetLeavesByIndex(deadlineMatcher(), &trillian.GetLeavesByIndexRequest{LeafIndex: []int64{1, 2}}).Return(&trillian.GetLeavesByIndexResponse{Status: okStatus, Leaves: rpcLeaves}, nil)

	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetEntriesHandler(c)

	req, err := http.NewRequest("GET", "/ct/v1/get-entries?start=1&end=2", nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// We should still have received the data though it failed to deserialize.
	if got, want := w.Code, http.StatusOK; got != want {
		t.Fatalf("expected %v for invalid merkle leaf result, got %v. Body: %v", want, got, w.Body)
	}

	var jsonMap map[string][]getEntriesEntry
	if err := json.Unmarshal(w.Body.Bytes(), &jsonMap); err != nil {
		t.Fatalf("Failed to unmarshal json response: %s", w.Body.Bytes())
	}

	if got, want := len(jsonMap), 1; got != want {
		t.Fatalf("Expected %d entry in outer json response, got %d", want, got)
	}
	entries := jsonMap["entries"]
	if got, want := len(entries), 2; got != want {
		t.Fatalf("Expected %d entries in json response, got %d", want, got)
	}

	// Both leaves were invalid but their data should have been passed through as is
	for l := 0; l < len(entries); l++ {
		if got, want := string(entries[l].LeafInput), invalidLeafString; got != want {
			t.Fatalf("Unexpected leaf data received, got %s, expected %s", got, want)
		}
	}
}

func TestGetEntries(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)

	// To pass validation the leaves we return from our dummy RPC must be valid serialized
	// ct.MerkleTreeLeaf objects
	merkleLeaf1 := ct.MerkleTreeLeaf{
		Version:          ct.V1,
		LeafType:         ct.TimestampedEntryLeafType,
		TimestampedEntry: ct.TimestampedEntry{Timestamp: 12345, EntryType: ct.X509LogEntryType, X509Entry: []byte("certdatacertdata"), Extensions: ct.CTExtensions{}}}

	merkleLeaf2 := ct.MerkleTreeLeaf{
		Version:          ct.V1,
		LeafType:         ct.TimestampedEntryLeafType,
		TimestampedEntry: ct.TimestampedEntry{Timestamp: 67890, EntryType: ct.X509LogEntryType, X509Entry: []byte("certdat2certdat2"), Extensions: ct.CTExtensions{}}}

	merkleBytes1, err1 := leafToBytes(merkleLeaf1)
	merkleBytes2, err2 := leafToBytes(merkleLeaf2)

	if err1 != nil || err2 != nil {
		t.Fatalf("error in test setup for get-entries: %v %v", err1, err2)
	}

	rpcLeaves := []*trillian.LeafProto{{LeafIndex: 1, LeafHash: []byte("hash"), LeafData: merkleBytes1, ExtraData: []byte("extra1")}, {LeafIndex: 2, LeafHash: []byte("hash"), LeafData: merkleBytes2, ExtraData: []byte("extra2")}}
	client.EXPECT().GetLeavesByIndex(deadlineMatcher(), &trillian.GetLeavesByIndexRequest{LeafIndex: []int64{1, 2}}).Return(&trillian.GetLeavesByIndexResponse{Status: okStatus, Leaves: rpcLeaves}, nil)

	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetEntriesHandler(c)

	req, err := http.NewRequest("GET", "/ct/v1/get-entries?start=1&end=2", nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusOK; got != want {
		t.Fatalf("Expected  %v for valid get-entries result, got %v. Body: %v", want, got, w.Body)
	}

	var jsonMap map[string][]getEntriesEntry
	if err := json.Unmarshal(w.Body.Bytes(), &jsonMap); err != nil {
		t.Fatalf("Failed to unmarshal json response: %s", w.Body.Bytes())
	}

	if got, want := len(jsonMap), 1; got != want {
		t.Fatalf("Expected %d entry in outer json response, got %d", want, got)
	}
	entries := jsonMap["entries"]
	if got, want := len(entries), 2; got != want {
		t.Fatalf("Expected %d entries in json response, got %d", want, got)
	}

	roundtripMerkleLeaf1, err1 := bytesToLeaf(entries[0].LeafInput)
	roundtripMerkleLeaf2, err2 := bytesToLeaf(entries[1].LeafInput)

	if err1 != nil || err2 != nil {
		t.Fatalf("one or both leaves failed to decode / deserialize: %v %v %v %v", err1, entries[0].LeafInput, err2, entries[1].LeafInput)
	}

	if got, want := *roundtripMerkleLeaf1, merkleLeaf1; !reflect.DeepEqual(got, want) {
		t.Fatalf("Leaf 1 mismatched on roundtrip, got %v, expected %v", got, want)
	}
	if got, want := entries[0].ExtraData, []byte("extra1"); !bytes.Equal(got, want) {
		t.Fatalf("Extra data mismatched on leaf 1, got %v, expected %v", got, want)
	}
	if got, want := *roundtripMerkleLeaf2, merkleLeaf2; !reflect.DeepEqual(got, want) {
		t.Fatalf("Leaf 2 mismatched on roundtrip, got %v, expected %v", got, want)
	}
	if got, want := entries[1].ExtraData, []byte("extra2"); !bytes.Equal(got, want) {
		t.Fatalf("Extra data mismatched on leaf 2, got %v, expected %v", got, want)
	}
}

func TestGetProofByHashBadRequests(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	// This is OK because the requests shouldn't get to the point where any RPCs are made on the mock
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetProofByHashHandler(c)

	for _, requestParamString := range getProofByHashBadRequests {
		req, err := http.NewRequest("GET", fmt.Sprintf("/ct/v1/proof-by-hash%s", requestParamString), nil)

		if err != nil {
			t.Fatal(err)
		}

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if got, want := w.Code, http.StatusBadRequest; got != want {
			t.Fatalf("Expected %v for get-proof-by-hash with params [%s], got %v. Body: %v", want, requestParamString, got, w.Body)
		}
	}
}

func TestGetProofByHashBackendFails(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	client.EXPECT().GetInclusionProofByHash(deadlineMatcher(), &trillian.GetInclusionProofByHashRequest{LeafHash: []byte("ahash"), TreeSize: 6, OrderBySequence: true}).Return(nil, errors.New("RPCFAIL"))
	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetProofByHashHandler(c)

	req, err := http.NewRequest("GET", "/ct/v1/proof-by-hash?tree_size=6&hash=YWhhc2g=", nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("Expected %v for get-proof-by-hash when backend fails, got %v. Body: %v", want, got, w.Body)
	}

	if !strings.Contains(w.Body.String(), "RPCFAIL") {
		t.Fatalf("Did not get expected backend error: %s\n%s", "RPCFAIL", w.Body)
	}
}

func TestGetProofByHashBackendMultipleProofs(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	proof1 := trillian.ProofProto{LeafIndex: 2, ProofNode: []*trillian.NodeProto{{NodeHash: []byte("abcdef")}, {NodeHash: []byte("ghijkl")}, {NodeHash: []byte("mnopqr")}}}
	proof2 := trillian.ProofProto{LeafIndex: 2, ProofNode: []*trillian.NodeProto{{NodeHash: []byte("ghijkl")}}}
	response := trillian.GetInclusionProofByHashResponse{Status: okStatus, Proof: []*trillian.ProofProto{&proof1, &proof2}}
	client := trillian.NewMockTrillianLogClient(mockCtrl)
	client.EXPECT().GetInclusionProofByHash(deadlineMatcher(), &trillian.GetInclusionProofByHashRequest{LeafHash: []byte("ahash"), TreeSize: 7, OrderBySequence: true}).Return(&response, nil)
	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetProofByHashHandler(c)

	req, err := http.NewRequest("GET", "/ct/v1/proof-by-hash?tree_size=7&hash=YWhhc2g=", nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Should be OK if backend returns multiple proofs and we should get the first one
	if got, want := w.Code, http.StatusOK; got != want {
		t.Fatalf("Expected %v for get-proof-by-hash (multiple), got %v. Body: %v", want, got, w.Body)
	}

	// Roundtrip the response and make sure it matches the expected one
	var resp getProofByHashResponse
	if err = json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to unmarshal json: %v, body: %v", err, w.Body.Bytes())
	}

	if got, want := resp, expectedInclusionProofByHash; !reflect.DeepEqual(got, want) {
		t.Fatalf("mismatched json response: expected %v got %v", want, got)
	}
}

func TestGetProofByHashBackendReturnsMissingHash(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	proof := trillian.ProofProto{LeafIndex: 2, ProofNode: []*trillian.NodeProto{{NodeHash: []byte("abcdef")}, {NodeHash: []byte{}}, {NodeHash: []byte("ghijkl")}}}
	response := trillian.GetInclusionProofByHashResponse{Status: okStatus, Proof: []*trillian.ProofProto{&proof}}
	client := trillian.NewMockTrillianLogClient(mockCtrl)
	client.EXPECT().GetInclusionProofByHash(deadlineMatcher(), &trillian.GetInclusionProofByHashRequest{LeafHash: []byte("ahash"), TreeSize: 9, OrderBySequence: true}).Return(&response, nil)
	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetProofByHashHandler(c)

	req, err := http.NewRequest("GET", "/ct/v1/proof-by-hash?tree_size=9&hash=YWhhc2g=", nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("Expected %v for get-proof-by-hash when backend returns missing hash, got %v. Body: %v", want, got, w.Body)
	}

	if !strings.Contains(w.Body.String(), "invalid proof") {
		t.Fatalf("Did not get expected backend error for invalid proof:\n%s", w.Body)
	}
}

func TestGetProofByHash(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	proof := trillian.ProofProto{LeafIndex: 2, ProofNode: []*trillian.NodeProto{{NodeHash: []byte("abcdef")}, {NodeHash: []byte("ghijkl")}, {NodeHash: []byte("mnopqr")}}}
	response := trillian.GetInclusionProofByHashResponse{Status: okStatus, Proof: []*trillian.ProofProto{&proof}}
	client := trillian.NewMockTrillianLogClient(mockCtrl)
	client.EXPECT().GetInclusionProofByHash(deadlineMatcher(), &trillian.GetInclusionProofByHashRequest{LeafHash: []byte("ahash"), TreeSize: 7, OrderBySequence: true}).Return(&response, nil)
	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetProofByHashHandler(c)

	req, err := http.NewRequest("GET", "/ct/v1/proof-by-hash?tree_size=7&hash=YWhhc2g=", nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusOK; got != want {
		t.Fatalf("Expected %v for get-proof-by-hash, got %v. Body: %v", want, got, w.Body)
	}

	// Roundtrip the response and make sure it matches the expected one
	var resp getProofByHashResponse
	if err = json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to unmarshal json: %v, body: %v", err, w.Body.Bytes())
	}

	if got, want := resp, expectedInclusionProofByHash; !reflect.DeepEqual(got, want) {
		t.Fatalf("mismatched json response: expected %v got %v", want, got)
	}
}

func TestGetSTHConsistencyBadParams(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	// This is OK because the requests shouldn't get to the point where any RPCs are made on the mock
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetSTHConsistencyHandler(c)

	for _, requestParamString := range getSTHConsistencyBadRequests {
		req, err := http.NewRequest("GET", fmt.Sprintf("/ct/v1/get-sth-consistency%s", requestParamString), nil)

		if err != nil {
			t.Fatal(err)
		}

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if got, want := w.Code, http.StatusBadRequest; got != want {
			t.Fatalf("Expected %v for get-sth-consistency with params [%s], got %v. Body: %v", want, requestParamString, got, w.Body)
		}
	}
}

func TestGetEntryAndProofBadParams(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	// This is OK because the requests shouldn't get to the point where any RPCs are made on the mock
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetEntryAndProofHandler(c)

	for _, requestParamString := range getEntryAndProofBadRequests {
		req, err := http.NewRequest("GET", fmt.Sprintf("/ct/v1/get-entry-and-proof%s", requestParamString), nil)

		if err != nil {
			t.Fatal(err)
		}

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if got, want := w.Code, http.StatusBadRequest; got != want {
			t.Fatalf("expected %v for get-entry-and-proof with params [%s], got %v. Body: %v", want, requestParamString, got, w.Body)
		}
	}
}

func TestGetSTHConsistencyBackendRPCFails(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	client.EXPECT().GetConsistencyProof(deadlineMatcher(), &trillian.GetConsistencyProofRequest{FirstTreeSize: 10, SecondTreeSize: 20}).Return(nil, errors.New("RPCFAIL"))
	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetSTHConsistencyHandler(c)

	req, err := http.NewRequest("GET", "/ct/v1/get-sth-consistency?first=10&second=20", nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("Expected %v for get-sth-consistency when backend fails, got %v. Body: %v", want, got, w.Body)
	}

	if !strings.Contains(w.Body.String(), "RPCFAIL") {
		t.Fatalf("Did not get expected backend error: %s\n%s", "RPCFAIL", w.Body)
	}
}

func TestGetEntryAndProofBackendFails(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)
	client.EXPECT().GetEntryAndProof(deadlineMatcher(), &trillian.GetEntryAndProofRequest{LeafIndex: 1, TreeSize: 3}).Return(nil, errors.New("RPCFAIL"))
	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetEntryAndProofHandler(c)

	req, err := http.NewRequest("GET", "/ct/v1/get-entry-and-proof?leaf_index=1&tree_size=3", nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("Expected %v for get-entry-and-proof when backend fails, got %v. Body: %v", want, got, w.Body)
	}

	if !strings.Contains(w.Body.String(), "RPCFAIL") {
		t.Fatalf("Did not get expected backend error: %s\n%s", "RPCFAIL", w.Body)
	}
}

func TestGetSTHConsistencyBackendReturnsInvalidProof(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	proof := trillian.ProofProto{LeafIndex: 2, ProofNode: []*trillian.NodeProto{{NodeHash: []byte("abcdef")}, {NodeHash: []byte{}}, {NodeHash: []byte("ghijkl")}}}
	response := trillian.GetConsistencyProofResponse{Status: okStatus, Proof: &proof}
	client := trillian.NewMockTrillianLogClient(mockCtrl)
	client.EXPECT().GetConsistencyProof(deadlineMatcher(), &trillian.GetConsistencyProofRequest{FirstTreeSize: 10, SecondTreeSize: 20}).Return(&response, nil)
	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetSTHConsistencyHandler(c)

	req, err := http.NewRequest("GET", "/ct/v1/get-sth-consistency?first=10&second=20", nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("Expected %v for get-sth-consistency when backend fails, got %v. Body: %v", want, got, w.Body)
	}

	if !strings.Contains(w.Body.String(), "invalid proof") {
		t.Fatalf("Did not get expected backend error: %s\n%s", "invalid proof", w.Body)
	}
}

func TestGetEntryAndProofBackendBadResponse(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// Omit the result data from the backend response, should cause the request to fail
	response := trillian.GetEntryAndProofResponse{Status: okStatus}
	client := trillian.NewMockTrillianLogClient(mockCtrl)
	client.EXPECT().GetEntryAndProof(deadlineMatcher(), &trillian.GetEntryAndProofRequest{LeafIndex: 1, TreeSize: 3}).Return(&response, nil)
	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetEntryAndProofHandler(c)

	req, err := http.NewRequest("GET", "/ct/v1/get-entry-and-proof?leaf_index=1&tree_size=3", nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusInternalServerError; got != want {
		t.Fatalf("Expected %v for get-entry-and-proof when backend fails, got %v. Body: %v", want, got, w.Body)
	}
}

func TestGetSTHConsistency(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	proof := trillian.ProofProto{LeafIndex: 2, ProofNode: []*trillian.NodeProto{{NodeHash: []byte("abcdef")}, {NodeHash: []byte("ghijkl")}, {NodeHash: []byte("mnopqr")}}}
	response := trillian.GetConsistencyProofResponse{Status: okStatus, Proof: &proof}
	client := trillian.NewMockTrillianLogClient(mockCtrl)
	client.EXPECT().GetConsistencyProof(deadlineMatcher(), &trillian.GetConsistencyProofRequest{FirstTreeSize: 10, SecondTreeSize: 20}).Return(&response, nil)
	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetSTHConsistencyHandler(c)

	req, err := http.NewRequest("GET", "/ct/v1/get-sth-consistency?first=10&second=20", nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusOK; got != want {
		t.Fatalf("Expected %v for get-sth-consistency when backend fails, got %v. Body: %v", want, got, w.Body)
	}

	// Roundtrip the response and make sure it matches
	var resp getSTHConsistencyResponse

	if err = json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to unmarshal json: %v, body: %v", err, w.Body.Bytes())
	}

	if got, want := resp, expectedSTHConsistencyProofByHash; !reflect.DeepEqual(got, want) {
		t.Fatalf("mismatched json response: expected %v got %v", want, got)
	}
}

func TestGetEntryAndProof(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	proof := trillian.ProofProto{LeafIndex: 2, ProofNode: []*trillian.NodeProto{{NodeHash: []byte("abcdef")}, {NodeHash: []byte("ghijkl")}, {NodeHash: []byte("mnopqr")}}}
	merkleLeaf := ct.MerkleTreeLeaf{
		Version:          ct.V1,
		LeafType:         ct.TimestampedEntryLeafType,
		TimestampedEntry: ct.TimestampedEntry{Timestamp: 12345, EntryType: ct.X509LogEntryType, X509Entry: []byte("certdatacertdata"), Extensions: ct.CTExtensions{}}}

	leafBytes, err := leafToBytes(merkleLeaf)

	if err != nil {
		t.Fatal("failed to build test merkle leaf data")
	}

	leafProto := trillian.LeafProto{LeafData: leafBytes, LeafHash: []byte("ahash"), ExtraData: []byte("extra")}
	response := trillian.GetEntryAndProofResponse{Status: okStatus, Proof: &proof, Leaf: &leafProto}
	client := trillian.NewMockTrillianLogClient(mockCtrl)
	client.EXPECT().GetEntryAndProof(deadlineMatcher(), &trillian.GetEntryAndProofRequest{LeafIndex: 1, TreeSize: 3}).Return(&response, nil)
	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetEntryAndProofHandler(c)

	req, err := http.NewRequest("GET", "/ct/v1/get-entry-and-proof?leaf_index=1&tree_size=3", nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if got, want := w.Code, http.StatusOK; got != want {
		t.Fatalf("Expected %v for get-entry-and-proof, got %v. Body: %v", want, got, w.Body)
	}

	// Roundtrip the response and make sure it matches what we expect
	var resp getEntryAndProofResponse
	if err = json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to unmarshal json: %v, body: %v", err, w.Body.Bytes())
	}

	// The result we expect after a roundtrip in the successful get entry and proof test
	expectedGetEntryAndProofResponse := getEntryAndProofResponse{
		LeafInput: leafBytes,
		ExtraData: []byte("extra"),
		AuditPath: [][]byte{[]byte("abcdef"), []byte("ghijkl"), []byte("mnopqr")}}

	if got, want := resp, expectedGetEntryAndProofResponse; !reflect.DeepEqual(got, want) {
		t.Fatalf("mismatched json response: expected %v got %v", want, got)
	}
}

func createJsonChain(t *testing.T, p PEMCertPool) io.Reader {
	var chain jsonChain

	for _, rawCert := range p.RawCertificates() {
		b64 := base64.StdEncoding.EncodeToString(rawCert.Raw)
		chain.Chain = append(chain.Chain, b64)
	}

	var buffer bytes.Buffer
	// It's tempting to avoid creating and flushing the intermediate writer but it doesn't work
	writer := bufio.NewWriter(&buffer)
	err := json.NewEncoder(writer).Encode(&chain)
	writer.Flush()

	if err != nil {
		t.Fatalf("Failed to create test json: %v", err)
	}

	return bufio.NewReader(&buffer)
}

func leafProtosForCert(t *testing.T, km crypto.KeyManager, certs []*x509.Certificate, merkleLeaf ct.MerkleTreeLeaf) []*trillian.LeafProto {
	var b bytes.Buffer
	if err := writeMerkleTreeLeaf(&b, merkleLeaf); err != nil {
		t.Fatalf("failed to serialize leaf: %v", err)
	}

	// This is a hash of the leaf data, not the the Merkle hash as defined in the RFC.
	leafHash := sha256.Sum256(b.Bytes())
	logEntry := NewCTLogEntry(merkleLeaf, certs)

	var b2 bytes.Buffer
	if err := logEntry.Serialize(&b2); err != nil {
		t.Fatalf("failed to serialize log entry: %v", err)
	}

	return []*trillian.LeafProto{{LeafHash: leafHash[:], LeafData: b.Bytes(), ExtraData: b2.Bytes()}}
}

type dlMatcher struct {
}

func deadlineMatcher() gomock.Matcher {
	return dlMatcher{}
}

func (d dlMatcher) Matches(x interface{}) bool {
	ctx, ok := x.(context.Context)
	if !ok {
		return false
	}

	deadlineTime, ok := ctx.Deadline()

	if !ok {
		return false // we never make RPC calls without a deadline set
	}

	return deadlineTime == fakeDeadlineTime
}

func (d dlMatcher) String() string {
	return fmt.Sprintf("deadline is %v", fakeDeadlineTime)
}

func makeAddPrechainRequest(t *testing.T, reqHandlers CTRequestHandlers, body io.Reader) *httptest.ResponseRecorder {
	handler := wrappedAddPreChainHandler(reqHandlers)
	return makeAddChainRequestInternal(t, handler, "add-pre-chain", body)
}

func makeAddChainRequest(t *testing.T, reqHandlers CTRequestHandlers, body io.Reader) *httptest.ResponseRecorder {
	handler := wrappedAddChainHandler(reqHandlers)
	return makeAddChainRequestInternal(t, handler, "add-chain", body)
}

func makeAddChainRequestInternal(t *testing.T, handler appHandler, path string, body io.Reader) *httptest.ResponseRecorder {
	req, err := http.NewRequest("POST", fmt.Sprintf("http://example.com/ct/v1/%s", path), body)
	if err != nil {
		t.Fatalf("Test request setup failed: %v", err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	return w
}

// getEntriesTestHelper is used for testing get-entries failure cases with arbitrary request params
func getEntriesTestHelper(t *testing.T, request string, expectedStatus int, explanation string) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	client := trillian.NewMockTrillianLogClient(mockCtrl)

	c := CTRequestHandlers{rpcClient: client, timeSource: fakeTimeSource, rpcDeadline: time.Millisecond * 500}
	handler := wrappedGetEntriesHandler(c)

	path := fmt.Sprintf("/ct/v1/get-entries?%s", request)
	req, err := http.NewRequest("GET", path, nil)

	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if expected, got := expectedStatus, w.Code; expected != got {
		t.Fatalf("expected status %d, got %d for test case %s", expected, got, explanation)
	}
}

func leafToBytes(leaf ct.MerkleTreeLeaf) ([]byte, error) {
	var buf bytes.Buffer
	err := writeMerkleTreeLeaf(&buf, leaf)

	if err != nil {
		return []byte{}, err
	}

	return buf.Bytes(), nil
}

func bytesToLeaf(leafBytes []byte) (*ct.MerkleTreeLeaf, error) {
	buf := bytes.NewBuffer(leafBytes)
	return ct.ReadMerkleTreeLeaf(buf)
}

func makeGetRootResponseForTest(stamp, treeSize int64, hash []byte) *trillian.GetLatestSignedLogRootResponse {
	return &trillian.GetLatestSignedLogRootResponse{Status: &trillian.TrillianApiStatus{StatusCode: trillian.TrillianApiStatusCode_OK},
		SignedLogRoot: &trillian.SignedLogRoot{
			TimestampNanos: stamp,
			TreeSize:       treeSize,
			RootHash:       hash}}
}
