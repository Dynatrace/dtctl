package azuremonitoringconfig

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

func (v *AzureConfig) UnmarshalJSON(data []byte) error {
	type plain AzureConfig
	extra, err := unknownfields.Unmarshal(data, (*plain)(v))
	if err != nil {
		return err
	}
	v.Extra = extra
	return nil
}

func (v AzureConfig) MarshalJSON() ([]byte, error) {
	type plain AzureConfig
	return unknownfields.Marshal(plain(v), v.Extra)
}

func (v AzureConfig) MarshalYAML() (any, error) {
	type plain AzureConfig
	return unknownfields.YAML(plain(v), v.Extra)
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

func (v *Labels) UnmarshalJSON(data []byte) error {
	type plain Labels
	extra, err := unknownfields.Unmarshal(data, (*plain)(v))
	if err != nil {
		return err
	}
	v.Extra = extra
	return nil
}

func (v Labels) MarshalJSON() ([]byte, error) {
	type plain Labels
	return unknownfields.Marshal(plain(v), v.Extra)
}

func (v Labels) MarshalYAML() (any, error) {
	type plain Labels
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
