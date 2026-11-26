package output

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/Dynatrace/dtctl/pkg/proto/livedebugger"
)

func TestParseSnapshotStringMap_ArrayFormat(t *testing.T) {
	raw := `[{"":0},{"metadata":1},{"rule_id":2}]`
	result, err := parseSnapshotStringMap(raw)
	if err != nil {
		t.Fatalf("parseSnapshotStringMap() error = %v", err)
	}

	if len(result) != 3 {
		t.Fatalf("len(result) = %d, want 3", len(result))
	}
	if result[0] != "" || result[1] != "metadata" || result[2] != "rule_id" {
		t.Fatalf("unexpected parsed cache: %#v", result)
	}
}

func TestParseSnapshotStringMap_MapFormat(t *testing.T) {
	raw := `{"":0,"metadata":1,"rule_id":2}`
	result, err := parseSnapshotStringMap(raw)
	if err != nil {
		t.Fatalf("parseSnapshotStringMap() error = %v", err)
	}

	if len(result) != 3 {
		t.Fatalf("len(result) = %d, want 3", len(result))
	}
	if result[1] != "metadata" || result[2] != "rule_id" {
		t.Fatalf("unexpected parsed cache: %#v", result)
	}
}

func TestParseSnapshotStringMap_ArrayFormatRejectsHugeIndex(t *testing.T) {
	raw := `[{"huge":100001}]`
	_, err := parseSnapshotStringMap(raw)
	if err == nil {
		t.Fatal("expected error for oversized index")
	}
	if !strings.Contains(err.Error(), "string map index too large") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseSnapshotStringMap_MapFormatRejectsHugeIndex(t *testing.T) {
	raw := `{"huge":100001}`
	_, err := parseSnapshotStringMap(raw)
	if err == nil {
		t.Fatal("expected error for oversized index")
	}
	if !strings.Contains(err.Error(), "string map index too large") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecodeSnapshotRecords_EnrichesRecord(t *testing.T) {
	stringsCache := []map[string]uint32{
		{"": 0},
		{"root": 1},
		{"java.lang.String": 2},
		{"hello": 3},
	}

	rootValue := &livedebugger.Variant2{
		VariantTypeMaxDepth:      uint32(livedebugger.Variant_VARIANT_STRING) << 1,
		OriginalTypeIndexInCache: 2,
		BytesIndexInCache:        3,
		OriginalSize:             5,
	}
	rootNamespace := &livedebugger.Variant2{
		VariantTypeMaxDepth:   uint32(livedebugger.Variant_VARIANT_NAMESPACE) << 1,
		AttributeNamesInCache: []uint32{1},
		AttributeValues:       []*livedebugger.Variant2{rootValue},
	}
	aug := &livedebugger.AugReportMessage{Arguments2: rootNamespace}
	payload, err := proto.Marshal(aug)
	if err != nil {
		t.Fatalf("marshal aug report: %v", err)
	}

	encoded := toBase64(payload)
	stringMapRaw, err := json.Marshal(stringsCache)
	if err != nil {
		t.Fatalf("marshal string map: %v", err)
	}

	records := []map[string]interface{}{
		{
			"snapshot.data":       encoded,
			"snapshot.string_map": string(stringMapRaw),
			"snapshot.id":         "abc",
		},
	}

	// Test without simplification (full mode)
	decoded := DecodeSnapshotRecords(records, false)
	if len(decoded) != 1 {
		t.Fatalf("expected 1 record, got %d", len(decoded))
	}
	record := decoded[0]

	// Raw encoded fields should be removed after successful decode
	if _, ok := record["snapshot.data"]; ok {
		t.Fatal("snapshot.data should be removed after decode")
	}
	if _, ok := record["snapshot.string_map"]; ok {
		t.Fatal("snapshot.string_map should be removed after decode")
	}

	parsedSnapshot, ok := record["parsed_snapshot"].(map[string]interface{})
	if !ok {
		t.Fatalf("parsed_snapshot missing: %#v", record)
	}
	root, ok := parsedSnapshot["root"].(map[string]interface{})
	if !ok {
		t.Fatalf("parsed_snapshot.root missing: %#v", parsedSnapshot)
	}
	if root["value"] != "hello" {
		t.Fatalf("parsed_snapshot.root.value = %#v, want hello", root["value"])
	}
	if _, exists := root["@CT"]; exists {
		t.Fatalf("parsed_snapshot.root.@CT should be removed: %#v", root)
	}
	if _, exists := root["@OS"]; exists {
		t.Fatalf("parsed_snapshot.root.@OS should be removed: %#v", root)
	}
	if root["type"] != "java.lang.String" {
		t.Fatalf("parsed_snapshot.root.type = %#v, want java.lang.String", root["type"])
	}

	if _, exists := record["snapshot.parsed"]; exists {
		t.Fatalf("snapshot.parsed should not be present: %#v", record)
	}
	if _, exists := record["snapshot.message_namespace"]; exists {
		t.Fatalf("snapshot.message_namespace should not be present: %#v", record)
	}
	if _, exists := record["snapshot.namespace_view"]; exists {
		t.Fatalf("snapshot.namespace_view should not be present: %#v", record)
	}
	if _, exists := record["snapshot.message_view"]; exists {
		t.Fatalf("snapshot.message_view should not be present: %#v", record)
	}
}

func TestDecodeSnapshotRecords_HandlesVariant2EdgeCases(t *testing.T) {
	stringMap := []map[string]uint32{
		{"": 0},
		{"msg": 1},
		{"formatted": 2},
		{"9223372036854775807123": 3},
		{"java.util.Set": 4},
		{"item-a": 5},
		{"item-b": 6},
	}

	formatted := &livedebugger.Variant2{
		VariantTypeMaxDepth:      uint32(livedebugger.Variant_VARIANT_FORMATTED_MESSAGE) << 1,
		BytesIndexInCache:        2,
		OriginalTypeIndexInCache: 1,
	}
	largeInt := &livedebugger.Variant2{
		VariantTypeMaxDepth:      uint32(livedebugger.Variant_VARIANT_LARGE_INT) << 1,
		BytesIndexInCache:        3,
		OriginalTypeIndexInCache: 1,
	}
	setList := &livedebugger.Variant2{
		VariantTypeMaxDepth:      uint32(livedebugger.Variant_VARIANT_SET) << 1,
		OriginalTypeIndexInCache: 4,
		CollectionValues: []*livedebugger.Variant2{
			{VariantTypeMaxDepth: uint32(livedebugger.Variant_VARIANT_STRING) << 1, BytesIndexInCache: 5, OriginalTypeIndexInCache: 1},
			{VariantTypeMaxDepth: uint32(livedebugger.Variant_VARIANT_STRING) << 1, BytesIndexInCache: 6, OriginalTypeIndexInCache: 1},
		},
	}
	errorVariant := &livedebugger.Variant2{
		VariantTypeMaxDepth: uint32(livedebugger.Variant_VARIANT_ERROR) << 1,
		ErrorValue: &livedebugger.Error2{ //nolint:all
			Message:    "boom",
			Parameters: formatted,
			Exc:        largeInt,
		},
	}
	timeVariant := &livedebugger.Variant2{
		VariantTypeMaxDepth:      uint32(livedebugger.Variant_VARIANT_TIME) << 1,
		OriginalTypeIndexInCache: 1,
		TimeValue:                &livedebugger.Timestamp{Seconds: 1700000000, Nanos: 123000000},
	}

	root := &livedebugger.Variant2{
		VariantTypeMaxDepth:   uint32(livedebugger.Variant_VARIANT_NAMESPACE) << 1,
		AttributeNamesInCache: []uint32{1, 2, 3, 4, 5},
		AttributeValues:       []*livedebugger.Variant2{formatted, largeInt, setList, errorVariant, timeVariant},
	}

	aug := &livedebugger.AugReportMessage{Arguments2: root, ReverseListOrder: true}
	payload, err := proto.Marshal(aug)
	if err != nil {
		t.Fatalf("marshal aug report: %v", err)
	}

	stringMapRaw, err := json.Marshal(stringMap)
	if err != nil {
		t.Fatalf("marshal string map: %v", err)
	}

	records := []map[string]interface{}{{
		"snapshot.data":       base64.StdEncoding.EncodeToString(payload),
		"snapshot.string_map": string(stringMapRaw),
	}}

	decoded := DecodeSnapshotRecords(records, false)
	if len(decoded) != 1 {
		t.Fatalf("expected 1 record, got %d", len(decoded))
	}
	record := decoded[0]
	parsed := record["parsed_snapshot"].(map[string]interface{})

	formattedOut := parsed["msg"].(map[string]interface{})
	if formattedOut["value"] != "formatted" {
		t.Fatalf("formatted value mismatch: %#v", formattedOut)
	}

	largeIntOut := parsed["formatted"].(map[string]interface{})
	if largeIntOut["value"] != "9223372036854775807123" {
		t.Fatalf("large int value mismatch: %#v", largeIntOut)
	}

	setOut := parsed["9223372036854775807123"].(map[string]interface{})
	if _, exists := setOut["@CT"]; exists {
		t.Fatalf("set output should not include @CT: %#v", setOut)
	}
	setValues := setOut["value"].([]interface{})
	first := setValues[0].(map[string]interface{})
	if first["value"] != "item-b" {
		t.Fatalf("expected reverse list order to apply to set values, got %#v", setValues)
	}

	errorOut := parsed["java.util.Set"].(map[string]interface{})
	if _, exists := errorOut["@CT"]; exists {
		t.Fatalf("error output should not include @CT: %#v", errorOut)
	}
	errorValue := errorOut["value"].(map[string]interface{})
	if errorValue["message"] != "boom" {
		t.Fatalf("error message mismatch: %#v", errorValue)
	}

	timeOut := parsed["item-a"].(map[string]interface{})
	if timeOut["value"] != "2023-11-14T22:13:20.123000Z" {
		t.Fatalf("timestamp format mismatch: %#v", timeOut)
	}
}

func TestDecodeSnapshotRecords_Simplified(t *testing.T) {
	stringsCache := []map[string]uint32{
		{"": 0},
		{"root": 1},
		{"java.lang.String": 2},
		{"hello": 3},
	}

	rootValue := &livedebugger.Variant2{
		VariantTypeMaxDepth:      uint32(livedebugger.Variant_VARIANT_STRING) << 1,
		OriginalTypeIndexInCache: 2,
		BytesIndexInCache:        3,
		OriginalSize:             5,
	}
	rootNamespace := &livedebugger.Variant2{
		VariantTypeMaxDepth:   uint32(livedebugger.Variant_VARIANT_NAMESPACE) << 1,
		AttributeNamesInCache: []uint32{1},
		AttributeValues:       []*livedebugger.Variant2{rootValue},
	}
	aug := &livedebugger.AugReportMessage{Arguments2: rootNamespace}
	payload, err := proto.Marshal(aug)
	if err != nil {
		t.Fatalf("marshal aug report: %v", err)
	}

	encoded := toBase64(payload)
	stringMapRaw, err := json.Marshal(stringsCache)
	if err != nil {
		t.Fatalf("marshal string map: %v", err)
	}

	records := []map[string]interface{}{
		{
			"snapshot.data":       encoded,
			"snapshot.string_map": string(stringMapRaw),
			"snapshot.id":         "abc",
		},
	}

	decoded := DecodeSnapshotRecords(records, true)
	record := decoded[0]

	// Raw encoded fields should be removed after successful decode
	if _, ok := record["snapshot.data"]; ok {
		t.Fatal("snapshot.data should be removed after decode")
	}
	if _, ok := record["snapshot.string_map"]; ok {
		t.Fatal("snapshot.string_map should be removed after decode")
	}

	// Non-snapshot fields should be preserved
	if record["snapshot.id"] != "abc" {
		t.Fatalf("snapshot.id = %v, want \"abc\"", record["snapshot.id"])
	}

	parsedSnapshot, ok := record["parsed_snapshot"].(map[string]interface{})
	if !ok {
		t.Fatalf("parsed_snapshot missing: %#v", record)
	}

	// With simplification, "root" should be the plain value "hello", not a wrapper
	rootVal, ok := parsedSnapshot["root"]
	if !ok {
		t.Fatalf("parsed_snapshot.root missing: %#v", parsedSnapshot)
	}
	if rootVal != "hello" {
		t.Fatalf("simplified root = %#v, want \"hello\"", rootVal)
	}
}

func TestSimplifySnapshotValues_Primitives(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  interface{}
	}{
		{
			name:  "integer wrapper",
			input: map[string]interface{}{"type": "Integer", "value": int64(42)},
			want:  int64(42),
		},
		{
			name:  "string wrapper",
			input: map[string]interface{}{"type": "java.lang.String", "value": "hello"},
			want:  "hello",
		},
		{
			name:  "boolean wrapper",
			input: map[string]interface{}{"type": "boolean", "value": true},
			want:  true,
		},
		{
			name:  "null wrapper",
			input: map[string]interface{}{"type": "null", "value": nil},
			want:  nil,
		},
		{
			name:  "float wrapper",
			input: map[string]interface{}{"type": "Double", "value": 3.14},
			want:  3.14,
		},
		{
			name:  "plain string passthrough",
			input: "just a string",
			want:  "just a string",
		},
		{
			name:  "plain int passthrough",
			input: 42,
			want:  42,
		},
		{
			name:  "nil passthrough",
			input: nil,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SimplifySnapshotValues(tt.input)
			if got != tt.want {
				t.Fatalf("SimplifySnapshotValues() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestSimplifySnapshotValues_Object(t *testing.T) {
	input := map[string]interface{}{
		"type": "com.example.User",
		"@attributes": map[string]interface{}{
			"name": map[string]interface{}{
				"type":  "java.lang.String",
				"value": "alice",
			},
			"age": map[string]interface{}{
				"type":  "Integer",
				"value": int64(30),
			},
		},
	}

	got := SimplifySnapshotValues(input)
	gotMap, ok := got.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T: %#v", got, got)
	}
	if gotMap["name"] != "alice" {
		t.Fatalf("name = %#v, want \"alice\"", gotMap["name"])
	}
	if gotMap["age"] != int64(30) {
		t.Fatalf("age = %#v, want 30", gotMap["age"])
	}
}

func TestSimplifySnapshotValues_List(t *testing.T) {
	input := map[string]interface{}{
		"type": "java.util.ArrayList",
		"value": []interface{}{
			map[string]interface{}{"type": "Integer", "value": int64(1)},
			map[string]interface{}{"type": "Integer", "value": int64(2)},
			map[string]interface{}{"type": "Integer", "value": int64(3)},
		},
	}

	got := SimplifySnapshotValues(input)
	gotSlice, ok := got.([]interface{})
	if !ok {
		t.Fatalf("expected slice, got %T: %#v", got, got)
	}
	if len(gotSlice) != 3 {
		t.Fatalf("len = %d, want 3", len(gotSlice))
	}
	if gotSlice[0] != int64(1) || gotSlice[1] != int64(2) || gotSlice[2] != int64(3) {
		t.Fatalf("list = %#v, want [1, 2, 3]", gotSlice)
	}
}

func TestSimplifySnapshotValues_Map(t *testing.T) {
	input := map[string]interface{}{
		"@CT":  dictType, // preserved by normalizeSnapshotFieldNames for isDictType
		"type": "java.util.HashMap",
		"value": []interface{}{
			[]interface{}{
				map[string]interface{}{"type": "java.lang.String", "value": "key1"},
				map[string]interface{}{"type": "Integer", "value": int64(100)},
			},
			[]interface{}{
				map[string]interface{}{"type": "java.lang.String", "value": "key2"},
				map[string]interface{}{"type": "Integer", "value": int64(200)},
			},
		},
	}

	got := SimplifySnapshotValues(input)
	gotMap, ok := got.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T: %#v", got, got)
	}
	if gotMap["key1"] != int64(100) {
		t.Fatalf("key1 = %#v, want 100", gotMap["key1"])
	}
	if gotMap["key2"] != int64(200) {
		t.Fatalf("key2 = %#v, want 200", gotMap["key2"])
	}
}

func TestSimplifySnapshotValues_Namespace(t *testing.T) {
	// A namespace (plain map without type wrappers) should recurse
	input := map[string]interface{}{
		"frame": map[string]interface{}{
			"filename": map[string]interface{}{"type": "String", "value": "App.java"},
			"line":     map[string]interface{}{"type": "Integer", "value": int64(42)},
		},
		"message": map[string]interface{}{"type": "String", "value": "test message"},
	}

	got := SimplifySnapshotValues(input)
	gotMap, ok := got.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T: %#v", got, got)
	}

	frame, ok := gotMap["frame"].(map[string]interface{})
	if !ok {
		t.Fatalf("frame should be map, got %T: %#v", gotMap["frame"], gotMap["frame"])
	}
	if frame["filename"] != "App.java" {
		t.Fatalf("frame.filename = %#v, want \"App.java\"", frame["filename"])
	}
	if frame["line"] != int64(42) {
		t.Fatalf("frame.line = %#v, want 42", frame["line"])
	}
	if gotMap["message"] != "test message" {
		t.Fatalf("message = %#v, want \"test message\"", gotMap["message"])
	}
}

func TestSimplifySnapshotValues_NestedObject(t *testing.T) {
	// Object with nested object in attributes
	input := map[string]interface{}{
		"type": "com.example.Order",
		"@attributes": map[string]interface{}{
			"id": map[string]interface{}{"type": "Integer", "value": int64(1)},
			"customer": map[string]interface{}{
				"type": "com.example.Customer",
				"@attributes": map[string]interface{}{
					"name": map[string]interface{}{"type": "String", "value": "bob"},
				},
			},
		},
	}

	got := SimplifySnapshotValues(input)
	gotMap, ok := got.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", got)
	}
	if gotMap["id"] != int64(1) {
		t.Fatalf("id = %#v, want 1", gotMap["id"])
	}
	customer, ok := gotMap["customer"].(map[string]interface{})
	if !ok {
		t.Fatalf("customer should be map, got %T", gotMap["customer"])
	}
	if customer["name"] != "bob" {
		t.Fatalf("customer.name = %#v, want \"bob\"", customer["name"])
	}
}

func TestDecodeSnapshotRecords_NoSnapshotData(t *testing.T) {
	records := []map[string]interface{}{
		{"some_field": "some_value"},
	}

	decoded := DecodeSnapshotRecords(records, true)
	if len(decoded) != 1 {
		t.Fatalf("expected 1 record, got %d", len(decoded))
	}
	if _, exists := decoded[0]["parsed_snapshot"]; exists {
		t.Fatal("parsed_snapshot should not be present for records without snapshot.data")
	}
}

func TestSummarizeSnapshotForTable(t *testing.T) {
	tests := []struct {
		name string
		rec  map[string]interface{}
		want string
	}{
		{
			name: "typical snapshot with frame and traceback",
			rec: map[string]interface{}{
				"parsed_snapshot": map[string]interface{}{
					"rookout": map[string]interface{}{
						"frame": map[string]interface{}{
							"filename": "App.java",
							"function": "main",
							"line":     42,
							"locals": map[string]interface{}{
								"x": 1, "y": 2, "z": 3,
							},
						},
						"traceback": []interface{}{"frame1", "frame2"},
					},
				},
			},
			want: "main() at App.java:42 | 3 locals, 2 frames",
		},
		{
			name: "no locals, no traceback",
			rec: map[string]interface{}{
				"parsed_snapshot": map[string]interface{}{
					"rookout": map[string]interface{}{
						"frame": map[string]interface{}{
							"filename": "App.java",
							"function": "run",
							"line":     10,
						},
					},
				},
			},
			want: "run() at App.java:10",
		},
		{
			name: "no parsed_snapshot — unchanged",
			rec: map[string]interface{}{
				"snapshot.id": "abc",
			},
			want: "", // no parsed_snapshot to summarize
		},
		{
			name: "unknown structure — fallback",
			rec: map[string]interface{}{
				"parsed_snapshot": map[string]interface{}{
					"custom": map[string]interface{}{
						"a": 1, "b": 2,
					},
				},
			},
			want: "<2 fields>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records := []map[string]interface{}{tt.rec}
			summarized := SummarizeSnapshotForTable(records)
			if len(summarized) != 1 {
				t.Fatalf("expected 1 record, got %d", len(summarized))
			}
			result, ok := summarized[0]["parsed_snapshot"].(string)
			if tt.want == "" {
				if ok {
					t.Fatalf("expected no summary, got %q", result)
				}
				return
			}
			if !ok {
				t.Fatalf("expected string summary, got %T: %v", summarized[0]["parsed_snapshot"], summarized[0]["parsed_snapshot"])
			}
			if result != tt.want {
				t.Fatalf("summary = %q, want %q", result, tt.want)
			}
			// Verify input records are not mutated
			if orig, exists := records[0]["parsed_snapshot"]; exists {
				if _, isString := orig.(string); isString {
					t.Fatal("original record was mutated: parsed_snapshot should still be a map")
				}
			}
		})
	}
}

func toBase64(b []byte) string {
	const encodeStd = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	if len(b) == 0 {
		return ""
	}

	result := make([]byte, 0, ((len(b)+2)/3)*4)
	for i := 0; i < len(b); i += 3 {
		var n uint32
		remain := len(b) - i
		n |= uint32(b[i]) << 16
		if remain > 1 {
			n |= uint32(b[i+1]) << 8
		}
		if remain > 2 {
			n |= uint32(b[i+2])
		}

		result = append(result,
			encodeStd[(n>>18)&63],
			encodeStd[(n>>12)&63],
		)
		if remain > 1 {
			result = append(result, encodeStd[(n>>6)&63])
		} else {
			result = append(result, '=')
		}
		if remain > 2 {
			result = append(result, encodeStd[n&63])
		} else {
			result = append(result, '=')
		}
	}

	return string(result)
}
