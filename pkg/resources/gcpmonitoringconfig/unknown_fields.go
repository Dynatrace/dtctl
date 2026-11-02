package gcpmonitoringconfig

import (
	"github.com/Dynatrace/dtctl/pkg/util/unknownfields"
)

// The monitoring-configuration document is read, modified and written back as
// a whole, so every struct in the value tree keeps the members it does not
// model (see package unknownfields). MarshalYAML keeps the reflected YAML
// shape that stable commands promise and appends Extra to it; it is temporary
// until YAML output uses JSON field names (see unknownfields.YAML).

func (v *Value) UnmarshalJSON(data []byte) error {
	type plain Value
	extra, err := unknownfields.Unmarshal(data, (*plain)(v))
	if err != nil {
		return err
	}
	v.Extra = extra
	return nil
}

func (v Value) MarshalJSON() ([]byte, error) {
	type plain Value
	return unknownfields.Marshal(plain(v), v.Extra)
}

func (v Value) MarshalYAML() (any, error) {
	type plain Value
	return unknownfields.YAML(plain(v), v.Extra)
}

func (v *GoogleCloudConfig) UnmarshalJSON(data []byte) error {
	type plain GoogleCloudConfig
	extra, err := unknownfields.Unmarshal(data, (*plain)(v))
	if err != nil {
		return err
	}
	v.Extra = extra
	return nil
}

func (v GoogleCloudConfig) MarshalJSON() ([]byte, error) {
	type plain GoogleCloudConfig
	return unknownfields.Marshal(plain(v), v.Extra)
}

func (v GoogleCloudConfig) MarshalYAML() (any, error) {
	type plain GoogleCloudConfig
	p := plain(v)
	// Before it became a pointer, observabilityscopesenabled was a plain bool
	// that `-o yaml` always printed, as false when absent. Keep printing it.
	if p.ObservabilityScopesEnabled == nil {
		off := false
		p.ObservabilityScopesEnabled = &off
	}
	return unknownfields.YAML(p, v.Extra)
}

func (v *TagFilter) UnmarshalJSON(data []byte) error {
	type plain TagFilter
	extra, err := unknownfields.Unmarshal(data, (*plain)(v))
	if err != nil {
		return err
	}
	v.Extra = extra
	return nil
}

func (v TagFilter) MarshalJSON() ([]byte, error) {
	type plain TagFilter
	return unknownfields.Marshal(plain(v), v.Extra)
}

func (v TagFilter) MarshalYAML() (any, error) {
	type plain TagFilter
	return unknownfields.YAML(plain(v), v.Extra)
}

func (v *DtLabelMapping) UnmarshalJSON(data []byte) error {
	type plain DtLabelMapping
	extra, err := unknownfields.Unmarshal(data, (*plain)(v))
	if err != nil {
		return err
	}
	v.Extra = extra
	return nil
}

func (v DtLabelMapping) MarshalJSON() ([]byte, error) {
	type plain DtLabelMapping
	return unknownfields.Marshal(plain(v), v.Extra)
}

func (v DtLabelMapping) MarshalYAML() (any, error) {
	type plain DtLabelMapping
	return unknownfields.YAML(plain(v), v.Extra)
}

func (v *FlagConfig) UnmarshalJSON(data []byte) error {
	type plain FlagConfig
	extra, err := unknownfields.Unmarshal(data, (*plain)(v))
	if err != nil {
		return err
	}
	v.Extra = extra
	return nil
}

func (v FlagConfig) MarshalJSON() ([]byte, error) {
	type plain FlagConfig
	return unknownfields.Marshal(plain(v), v.Extra)
}

func (v FlagConfig) MarshalYAML() (any, error) {
	type plain FlagConfig
	return unknownfields.YAML(plain(v), v.Extra)
}

func (v *MetricSource) UnmarshalJSON(data []byte) error {
	type plain MetricSource
	extra, err := unknownfields.Unmarshal(data, (*plain)(v))
	if err != nil {
		return err
	}
	v.Extra = extra
	return nil
}

func (v MetricSource) MarshalJSON() ([]byte, error) {
	type plain MetricSource
	return unknownfields.Marshal(plain(v), v.Extra)
}

func (v MetricSource) MarshalYAML() (any, error) {
	type plain MetricSource
	return unknownfields.YAML(plain(v), v.Extra)
}

func (v *Metric) UnmarshalJSON(data []byte) error {
	type plain Metric
	extra, err := unknownfields.Unmarshal(data, (*plain)(v))
	if err != nil {
		return err
	}
	v.Extra = extra
	return nil
}

func (v Metric) MarshalJSON() ([]byte, error) {
	type plain Metric
	return unknownfields.Marshal(plain(v), v.Extra)
}

func (v Metric) MarshalYAML() (any, error) {
	type plain Metric
	return unknownfields.YAML(plain(v), v.Extra)
}

func (v *Credential) UnmarshalJSON(data []byte) error {
	type plain Credential
	extra, err := unknownfields.Unmarshal(data, (*plain)(v))
	if err != nil {
		return err
	}
	v.Extra = extra
	return nil
}

func (v Credential) MarshalJSON() ([]byte, error) {
	type plain Credential
	return unknownfields.Marshal(plain(v), v.Extra)
}

func (v Credential) MarshalYAML() (any, error) {
	type plain Credential
	return unknownfields.YAML(plain(v), v.Extra)
}
