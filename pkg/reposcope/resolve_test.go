package reposcope

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// monorepoFile is three path-bound entries, as `set … --path` writes them.
func monorepoFile() *File {
	return &File{Environments: map[string][]Entry{prodHost: {
		{Name: "checkout", Path: "services/checkout", ServiceNames: []string{"checkout"}},
		{Name: "checkout-v2", Path: "services/checkout-v2", ServiceNames: []string{"checkout-v2"}},
		{Name: "ledger", Path: "services/ledger", ServiceNames: []string{"ledger"}},
		{Name: "ledger-close", Path: "services/ledger/close", ServiceNames: []string{"ledger-close"}},
	}}}
}

func withRepositoryWide(f *File) *File {
	f.Upsert(prodHost, Entry{Name: "shop", ServiceNames: []string{"shop"}})
	return f
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name   string
		file   *File
		host   string
		dir    string
		pick   string
		entry  string // "" for no entry
		reason string
		others []string
	}{
		{name: "exact path", file: monorepoFile(), host: prodHost, dir: "services/checkout",
			entry: "checkout", others: []string{"checkout-v2", "ledger", "ledger-close"}},
		{name: "below a path", file: monorepoFile(), host: prodHost, dir: "services/checkout/internal/api",
			entry: "checkout", others: []string{"checkout-v2", "ledger", "ledger-close"}},
		{name: "a prefix that is not a path segment", file: monorepoFile(), host: prodHost, dir: "services/checkout-v2/cmd",
			entry: "checkout-v2", others: []string{"checkout", "ledger", "ledger-close"}},
		{name: "the longest path wins", file: monorepoFile(), host: prodHost, dir: "services/ledger/close/job",
			entry: "ledger-close", others: []string{"checkout", "checkout-v2", "ledger"}},
		{name: "no entry covers the directory", file: monorepoFile(), host: prodHost, dir: ".",
			reason: `no entry covers "."; entries here cover services/checkout, services/checkout-v2, services/ledger, services/ledger/close; pick one with --repo-scope <name>`,
			others: []string{"checkout", "checkout-v2", "ledger", "ledger-close"}},
		{name: "the repository-wide entry catches the rest", file: withRepositoryWide(monorepoFile()), host: prodHost, dir: "docs",
			entry: "shop", others: []string{"checkout", "checkout-v2", "ledger", "ledger-close"}},
		{name: "a path entry beats the repository-wide one", file: withRepositoryWide(monorepoFile()), host: prodHost, dir: "services/ledger",
			entry: "ledger", others: []string{"checkout", "checkout-v2", "ledger-close", "shop"}},
		{name: "no entries for this host", file: monorepoFile(), host: stgHost, dir: "services/checkout",
			reason: "no entry for stg98765.apps.dynatrace.com; the file defines: abc12345.apps.dynatrace.com"},
		{name: "selected by name from anywhere", file: monorepoFile(), host: prodHost, dir: ".", pick: "ledger",
			entry: "ledger", reason: `selected by name; its path "services/ledger" does not cover "."`,
			others: []string{"checkout", "checkout-v2", "ledger-close"}},
		{name: "selected by name where it applies anyway", file: monorepoFile(), host: prodHost, dir: "services/ledger", pick: "ledger",
			entry: "ledger", others: []string{"checkout", "checkout-v2", "ledger-close"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Resolve(tt.file, tt.host, tt.dir, tt.pick)
			require.NoError(t, err)
			assert.Equal(t, tt.entry != "", r.Linked)
			assert.Equal(t, FileName, r.File)
			assert.Equal(t, tt.host, r.Environment)
			assert.Equal(t, tt.dir, r.Dir)
			if tt.entry == "" {
				assert.Nil(t, r.Entry)
			} else {
				require.NotNil(t, r.Entry)
				assert.Equal(t, tt.entry, r.Entry.Name)
			}
			assert.Equal(t, tt.reason, r.Reason)
			assert.Equal(t, tt.others, r.Others)
		})
	}
}

func TestResolve_UnknownName(t *testing.T) {
	_, err := Resolve(monorepoFile(), prodHost, ".", "ledgr")
	var notFound *NotFoundError
	require.ErrorAs(t, err, &notFound)
	assert.Equal(t, &NotFoundError{Name: "ledgr", Host: prodHost, Known: []string{"checkout", "checkout-v2", "ledger", "ledger-close"}}, notFound)
	assert.Equal(t, `repo scope "ledgr" not found for abc12345.apps.dynatrace.com (entries: checkout, checkout-v2, ledger, ledger-close)`, err.Error())

	_, err = Resolve(monorepoFile(), stgHost, ".", "checkout")
	require.ErrorAs(t, err, &notFound)
	assert.Empty(t, notFound.Known)
	assert.Equal(t, `repo scope "checkout" not found: .dtctl-repo-scope.yaml defines no entry for stg98765.apps.dynatrace.com`, err.Error())
}

func TestResolve_PathCase(t *testing.T) {
	prev := foldPathCase
	t.Cleanup(func() { foldPathCase = prev })

	foldPathCase = true
	r, err := Resolve(monorepoFile(), prodHost, "Services/Checkout/api", "")
	require.NoError(t, err)
	require.NotNil(t, r.Entry, "a case-insensitive filesystem folds case")
	assert.Equal(t, "checkout", r.Entry.Name)

	foldPathCase = false
	r, err = Resolve(monorepoFile(), prodHost, "Services/Checkout/api", "")
	require.NoError(t, err)
	assert.Nil(t, r.Entry, "a case-sensitive one does not")
}

func TestResolve_Status(t *testing.T) {
	f := exampleFile()
	f.unknown = []string{`"discovered" on line 8`}
	r, err := Resolve(f, prodHost, "services/checkout/internal", "")
	require.NoError(t, err)
	assert.Equal(t, &Status{
		Linked:      true,
		File:        FileName,
		Environment: prodHost,
		Dir:         "services/checkout/internal",
		Entry:       &f.Environments[prodHost][0],
		Filters: map[string]string{
			"logs":  `in(dt.entity.process_group, array("PROCESS_GROUP-FEDCBA9876543210")) or service.name == "checkout" or (k8s.namespace.name == "payments" and k8s.workload.name == "checkout")`,
			"spans": `in(dt.entity.service, array("SERVICE-0123456789ABCDEF")) or service.name == "checkout" or (k8s.namespace.name == "payments" and k8s.workload.name == "checkout")`,
		},
		Warning: `unknown key "discovered" on line 8 (a typo, or written by a newer dtctl)`,
		Others:  []string{"ledger"},
		Hosts:   []string{prodHost, stgHost},
	}, r)
}
