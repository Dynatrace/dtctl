package output

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/Dynatrace/dtctl/pkg/proto/livedebugger"
)

const maxSnapshotStringMapIndex = 100_000

// DecodeSnapshotRecords decodes snapshot.data protobuf payloads in query records,
// adding a "parsed_snapshot" field to each record that contains snapshot data.
// The decoded tree uses normalized field names (type/value instead of @OT/@value).
// If simplify is true, variant wrappers are flattened to plain values
// (e.g., {"type": "Integer", "value": 42} becomes just 42).
func DecodeSnapshotRecords(records []map[string]interface{}, simplify bool) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		enriched := enrichSnapshotRecord(rec)
		if parsed, ok := enriched["parsed_snapshot"]; ok {
			if simplify {
				enriched["parsed_snapshot"] = SimplifySnapshotValues(parsed)
			} else {
				// Strip internal @CT markers from full-decode output;
				// they are only needed by SimplifySnapshotValues.
				enriched["parsed_snapshot"] = stripCTFields(parsed)
			}
		}
		result = append(result, enriched)
	}
	return result
}

// snapshotTableColumns defines the columns shown in table/CSV output when --decode-snapshots is active.
// Order matters: columns are displayed in this order.
var snapshotTableColumns = []string{
	"timestamp",
	"snapshot.id",
	"snapshot.message",
	"code.filepath",
	"code.function",
	"code.line.number",
	"session.id",
	"thread.name",
	"parsed_snapshot",
}

// SummarizeSnapshotForTable replaces the parsed_snapshot map in each record with a
// human-readable summary string, and filters to only the most relevant columns
// for readable table/CSV output.
// Example summary: "send() at PpxSessionSender.java:62 | 4 locals, 30 frames"
func SummarizeSnapshotForTable(records []map[string]interface{}) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		// Filter to relevant columns only, preserving display order
		filtered := make(map[string]interface{}, len(snapshotTableColumns))
		for _, col := range snapshotTableColumns {
			if v, ok := rec[col]; ok {
				filtered[col] = v
			}
		}

		// Summarize the parsed_snapshot to a human-readable string
		if parsed, ok := filtered["parsed_snapshot"]; ok {
			if parsedMap, ok := parsed.(map[string]interface{}); ok {
				filtered["parsed_snapshot"] = summarizeSnapshot(parsedMap)
			}
		}

		result = append(result, filtered)
	}
	return result
}

func summarizeSnapshot(parsed map[string]interface{}) string {
	// Navigate to rookout.frame
	rookout, _ := parsed["rookout"].(map[string]interface{})
	if rookout == nil {
		return summarizeFallback(parsed)
	}

	frame, _ := rookout["frame"].(map[string]interface{})

	var parts []string

	// Build "function() at file:line" part
	if frame != nil {
		fn, _ := frame["function"].(string)
		file, _ := frame["filename"].(string)
		line := frame["line"]

		if fn != "" || file != "" {
			location := ""
			if fn != "" {
				location = fn + "()"
			}
			if file != "" {
				fileLine := file
				if line != nil {
					fileLine = fmt.Sprintf("%s:%v", file, line)
				}
				if location != "" {
					location += " at " + fileLine
				} else {
					location = fileLine
				}
			}
			parts = append(parts, location)
		}

		// Count locals
		if locals, ok := frame["locals"].(map[string]interface{}); ok && len(locals) > 0 {
			parts = append(parts, fmt.Sprintf("%d locals", len(locals)))
		}
	}

	// Count traceback frames
	if tb, ok := rookout["traceback"].([]interface{}); ok && len(tb) > 0 {
		parts = append(parts, fmt.Sprintf("%d frames", len(tb)))
	}

	if len(parts) == 0 {
		return summarizeFallback(parsed)
	}

	// Join: first part is the location, rest are stats separated by ", "
	if len(parts) == 1 {
		return parts[0]
	}
	return parts[0] + " | " + strings.Join(parts[1:], ", ")
}

func summarizeFallback(parsed map[string]interface{}) string {
	n := countLeaves(parsed)
	return fmt.Sprintf("<%d fields>", n)
}

func countLeaves(v interface{}) int {
	switch typed := v.(type) {
	case map[string]interface{}:
		n := 0
		for _, val := range typed {
			n += countLeaves(val)
		}
		return n
	case []interface{}:
		n := 0
		for _, val := range typed {
			n += countLeaves(val)
		}
		return n
	default:
		return 1
	}
}

// SimplifySnapshotValues flattens variant type wrappers to plain values.
// The input should be a normalized decoded snapshot tree (after normalizeSnapshotFieldNames).
//
// Simplification rules:
//   - Primitives: {"type": "Integer", "value": 42} → 42
//   - Strings: {"type": "String", "value": "hello"} → "hello"
//   - Objects: {"type": "MyClass", "@attributes": {field: ...}} → {field: ...} (recurse)
//   - Lists/Sets: {"type": "ArrayList", "value": [...]} → [...] (recurse into elements)
//   - Maps: {"type": "HashMap", "value": [[k,v],...]} → {k: v, ...} (convert pairs to map)
//   - Namespaces (plain maps without type wrappers): recurse into each value
func SimplifySnapshotValues(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return simplifyMap(typed)
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = SimplifySnapshotValues(item)
		}
		return out
	default:
		return value
	}
}

func simplifyMap(m map[string]interface{}) interface{} {
	// If this map doesn't have "type" or "value" or "@attributes" or "@CT",
	// it's a plain namespace/dict. Just recurse into its values.
	_, hasType := m["type"]
	_, hasValue := m["value"]
	_, hasAttrs := m["@attributes"]
	_, hasCT := m["@CT"]

	if !hasType && !hasValue && !hasAttrs && !hasCT {
		// Plain namespace map — recurse into values
		out := make(map[string]interface{}, len(m))
		for k, v := range m {
			out[k] = SimplifySnapshotValues(v)
		}
		return out
	}

	// If it has @attributes, this is an object/enum — return simplified attributes
	if hasAttrs {
		attrs, ok := m["@attributes"].(map[string]interface{})
		if ok && len(attrs) > 0 {
			out := make(map[string]interface{}, len(attrs))
			for k, v := range attrs {
				out[k] = SimplifySnapshotValues(v)
			}
			return out
		}
		// Object with no attributes — fall through to value-based simplification
	}

	// Has "value" — extract and simplify
	if hasValue {
		val := m["value"]

		// Check if this is a map type (value is list of [key, value] pairs)
		if pairs, ok := val.([]interface{}); ok && isDictType(m) {
			return simplifyMapPairs(pairs)
		}

		// Recurse into the value
		return SimplifySnapshotValues(val)
	}

	// Has "type" but no "value" and no "@attributes" — return nil
	return nil
}

// isDictType checks if a normalized variant map represents a dict/map type
// by inspecting the preserved @CT (container type) field.
func isDictType(m map[string]interface{}) bool {
	ct, ok := m["@CT"]
	if !ok {
		return false
	}
	// @CT is an integer constant; dictType = 12
	ctInt, ok := ct.(int)
	return ok && ctInt == dictType
}

// stripCTFields recursively removes internal @CT markers from a decoded snapshot tree.
// These markers are preserved during normalization for isDictType to use,
// but should not appear in the final output.
func stripCTFields(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for k, v := range typed {
			if k == "@CT" {
				continue
			}
			out[k] = stripCTFields(v)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = stripCTFields(item)
		}
		return out
	default:
		return value
	}
}

// simplifyMapPairs converts [[key, value], ...] pairs into a map.
func simplifyMapPairs(pairs []interface{}) interface{} {
	out := make(map[string]interface{}, len(pairs))
	for _, pair := range pairs {
		pairSlice, ok := pair.([]interface{})
		if !ok || len(pairSlice) != 2 {
			continue
		}
		key := SimplifySnapshotValues(pairSlice[0])
		val := SimplifySnapshotValues(pairSlice[1])
		// Convert key to string for map key
		keyStr := fmt.Sprintf("%v", key)
		out[keyStr] = val
	}
	return out
}

func enrichSnapshotRecord(record map[string]interface{}) map[string]interface{} {
	data, okData := record["snapshot.data"].(string)
	if !okData || data == "" {
		return record
	}

	out := make(map[string]interface{}, len(record)+1)
	for k, v := range record {
		out[k] = v
	}

	var indexToString []string
	stringMapRaw, hasStringMap := record["snapshot.string_map"].(string)
	if hasStringMap && stringMapRaw != "" {
		parsedStrings, err := parseSnapshotStringMap(stringMapRaw)
		if err != nil {
			indexToString = nil
		} else {
			indexToString = parsedStrings
		}
	}

	decoded, err := decodeSnapshotDataToGeneric(data, indexToString)
	if err != nil {
		out["snapshot.decode_error"] = err.Error()
		return out
	}

	out["parsed_snapshot"] = decoded
	// Remove the raw encoded fields — they are redundant with parsed_snapshot
	delete(out, "snapshot.data")
	delete(out, "snapshot.string_map")
	return out
}

func parseSnapshotStringMap(raw string) ([]string, error) {
	var asArray []map[string]uint32
	if err := json.Unmarshal([]byte(raw), &asArray); err == nil {
		maxIndex := 0
		for _, item := range asArray {
			for _, idx := range item {
				if int(idx) > maxIndex {
					maxIndex = int(idx)
				}
				break
			}
		}
		if maxIndex > maxSnapshotStringMapIndex {
			return nil, fmt.Errorf("string map index too large: %d", maxIndex)
		}

		result := make([]string, maxIndex+1)
		for _, item := range asArray {
			for value, idx := range item {
				result[idx] = value
				break
			}
		}
		return result, nil
	}

	var asMap map[string]uint32
	if err := json.Unmarshal([]byte(raw), &asMap); err == nil {
		maxIndex := 0
		for _, idx := range asMap {
			if int(idx) > maxIndex {
				maxIndex = int(idx)
			}
		}
		if maxIndex > maxSnapshotStringMapIndex {
			return nil, fmt.Errorf("string map index too large: %d", maxIndex)
		}
		result := make([]string, maxIndex+1)
		for value, idx := range asMap {
			result[idx] = value
		}
		return result, nil
	}

	return nil, fmt.Errorf("invalid JSON format for snapshot.string_map")
}

func decodeSnapshotDataToGeneric(data string, stringCache []string) (interface{}, error) {
	decodedData, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode snapshot.data base64: %w", err)
	}

	rawAugReport := new(livedebugger.AugReportMessage)
	err = proto.Unmarshal(decodedData, rawAugReport)
	if err != nil {
		return nil, fmt.Errorf("failed to decode snapshot.data protobuf: %w", err)
	}

	if rawAugReport.GetArguments2() == nil {
		return nil, fmt.Errorf("got aug report without arguments2")
	}

	if len(rawAugReport.GetStringsCache()) == 0 && len(stringCache) == 0 {
		return nil, fmt.Errorf("got aug report without strings cache")
	}

	if len(stringCache) > 0 {
		rawAugReport.StringsCache = toStringCacheEntries(stringCache)
	}

	caches := newVariant2CachesFromAugReport(rawAugReport)
	decoded := variant2ToDict(rawAugReport.GetArguments2(), caches, rawAugReport.GetReverseListOrder())
	return normalizeSnapshotFieldNames(decoded), nil
}

func normalizeSnapshotFieldNames(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			switch key {
			case "@OS", "@max_depth":
				continue
			case "@CT":
				// Preserve @CT for simplification to use (isDictType).
				// SimplifySnapshotValues strips it from the final output.
				out["@CT"] = item
			case "@OT":
				out["type"] = normalizeSnapshotFieldNames(item)
			case "@value":
				out["value"] = normalizeSnapshotFieldNames(item)
			default:
				out[key] = normalizeSnapshotFieldNames(item)
			}
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i := range typed {
			out[i] = normalizeSnapshotFieldNames(typed[i])
		}
		return out
	default:
		return value
	}
}

type variant2Caches struct {
	stringCaches map[int]string
	buffersCache map[int][]byte
}

func newVariant2CachesFromAugReport(augReport *livedebugger.AugReportMessage) *variant2Caches {
	stringCaches := make(map[int]string)
	buffersCache := make(map[int][]byte)

	for _, entry := range augReport.GetStringsCache() {
		stringCaches[int(entry.GetValue())] = entry.GetKey()
	}

	for idx, value := range augReport.GetBufferCacheIndexes() {
		if idx < len(augReport.GetBufferCacheBuffers()) {
			buffersCache[int(value)] = augReport.GetBufferCacheBuffers()[idx]
		}
	}

	return &variant2Caches{stringCaches: stringCaches, buffersCache: buffersCache}
}

func (c *variant2Caches) getStringFromCache(index int) string {
	if value, ok := c.stringCaches[index]; ok {
		return value
	}
	return ""
}

func (c *variant2Caches) getBufferFromCache(index int) []byte {
	if value, ok := c.buffersCache[index]; ok {
		return value
	}
	return nil
}

func toStringCacheEntries(stringCache []string) []*livedebugger.StringCacheEntry {
	entries := make([]*livedebugger.StringCacheEntry, 0, len(stringCache))
	for idx, key := range stringCache {
		entries = append(entries, &livedebugger.StringCacheEntry{Key: key, Value: uint32(idx)})
	}
	return entries
}

func variant2ToDict(v *livedebugger.Variant2, caches *variant2Caches, reverseLists bool) map[string]interface{} {
	if v == nil {
		return map[string]interface{}{"@CT": nullType, "@value": nil}
	}

	dict := map[string]interface{}{
		"@OT": caches.getStringFromCache(int(v.GetOriginalTypeIndexInCache())),
	}
	if (v.GetVariantTypeMaxDepth() & 1) == 1 {
		dict["@max_depth"] = true
	}

	variantType := livedebugger.Variant_Type(v.GetVariantTypeMaxDepth() >> 1)
	originalType := strings.ToLower(caches.getStringFromCache(int(v.GetOriginalTypeIndexInCache())))

	switch variantType {
	case livedebugger.Variant_VARIANT_NONE, livedebugger.Variant_VARIANT_UNDEFINED:
		dict["@CT"] = nullType
		dict["@value"] = nil
	case livedebugger.Variant_VARIANT_INT, livedebugger.Variant_VARIANT_LONG:
		if strings.HasPrefix(originalType, "bool") {
			dict["@CT"] = boolType
			dict["@value"] = v.GetLongValue() != 0
		} else {
			dict["@CT"] = intType
			dict["@value"] = int64ToSafeJSNumber(v.GetLongValue())
		}
	case livedebugger.Variant_VARIANT_DOUBLE:
		dict["@CT"] = floatType
		doubleValue := v.GetDoubleValue()
		switch {
		case math.IsNaN(doubleValue):
			dict["@value"] = "NaN"
		case math.IsInf(doubleValue, 1):
			dict["@value"] = "+Inf"
		case math.IsInf(doubleValue, -1):
			dict["@value"] = "-Inf"
		default:
			dict["@value"] = doubleValue
		}
	case livedebugger.Variant_VARIANT_COMPLEX:
		dict["@CT"] = complexType
		complexValue := v.GetComplexValue()
		if complexValue == nil {
			dict["@value"] = map[string]interface{}{"real": 0.0, "imaginary": 0.0}
			break
		}
		realValue := complexValue.GetReal()
		imaginaryValue := complexValue.GetImaginary()
		dict["@value"] = map[string]interface{}{
			"real":      realValue,
			"imaginary": imaginaryValue,
		}
	case livedebugger.Variant_VARIANT_STRING, livedebugger.Variant_VARIANT_MASKED:
		dict["@CT"] = stringType
		dict["@value"] = caches.getStringFromCache(int(v.GetBytesIndexInCache()))
		dict["@OS"] = v.GetOriginalSize()
	case livedebugger.Variant_VARIANT_LARGE_INT:
		dict["@CT"] = intType
		dict["@value"] = caches.getStringFromCache(int(v.GetBytesIndexInCache()))
	case livedebugger.Variant_VARIANT_BINARY:
		dict["@CT"] = binaryType
		dict["@value"] = caches.getBufferFromCache(int(v.GetBytesIndexInCache()))
		dict["@OS"] = v.GetOriginalSize()
	case livedebugger.Variant_VARIANT_TIME:
		dict["@CT"] = datetimeType
		dict["@value"] = formatRookoutTimestamp(v.GetTimeValue())
	case livedebugger.Variant_VARIANT_ENUM:
		dict["@CT"] = enumType
		dict["@value"] = map[string]interface{}{
			"@ordinal_value": int32(v.GetLongValue()),
			"@type_name":     caches.getStringFromCache(int(v.GetOriginalTypeIndexInCache())),
			"@value":         caches.getStringFromCache(int(v.GetBytesIndexInCache())),
		}
		addAttributesToDict(dict, v, caches, reverseLists)
	case livedebugger.Variant_VARIANT_LIST, livedebugger.Variant_VARIANT_SET:
		if variantType == livedebugger.Variant_VARIANT_SET || originalType == "set" {
			dict["@CT"] = setType
		} else {
			dict["@CT"] = listType
		}
		listValues := make([]interface{}, len(v.GetCollectionValues()))
		for i, value := range v.GetCollectionValues() {
			listValues[i] = variant2ToDict(value, caches, reverseLists)
		}
		if reverseLists {
			reverseInterfaces(listValues)
		}
		dict["@value"] = listValues
		dict["@OS"] = v.GetOriginalSize()
		addAttributesToDict(dict, v, caches, reverseLists)
	case livedebugger.Variant_VARIANT_MAP:
		dict["@CT"] = dictType
		mapEntries := make([]interface{}, len(v.GetCollectionKeys()))
		for i, key := range v.GetCollectionKeys() {
			mapEntries[i] = []interface{}{
				variant2ToDict(key, caches, reverseLists),
				variant2ToDict(v.GetCollectionValues()[i], caches, reverseLists),
			}
		}
		dict["@OS"] = v.GetOriginalSize()
		dict["@value"] = mapEntries
		addAttributesToDict(dict, v, caches, reverseLists)
	case livedebugger.Variant_VARIANT_OBJECT:
		dict["@CT"] = userObjectType
		addAttributesToDict(dict, v, caches, reverseLists)
	case livedebugger.Variant_VARIANT_NAMESPACE:
		namespaceDict := make(map[string]interface{})
		for i, attrNameIndex := range v.GetAttributeNamesInCache() {
			if i < len(v.GetAttributeValues()) {
				attrName := caches.getStringFromCache(int(attrNameIndex))
				namespaceDict[attrName] = variant2ToDict(v.GetAttributeValues()[i], caches, reverseLists)
			}
		}
		return namespaceDict
	case livedebugger.Variant_VARIANT_UKNOWN_OBJECT:
		dict["@CT"] = unknownObjectType
		addAttributesToDict(dict, v, caches, reverseLists)
	case livedebugger.Variant_VARIANT_ERROR:
		errorValue := v.GetErrorValue()
		if errorValue == nil {
			dict["@OT"] = "Error"
			dict["@CT"] = stringType
			dict["@value"] = "<Error>"
			break
		}
		return map[string]interface{}{
			"@CT": namespaceType,
			"@value": map[string]interface{}{
				"message":    errorValue.GetMessage(),
				"parameters": variant2ToDict(errorValue.GetParameters(), caches, reverseLists),
				"exc":        variant2ToDict(errorValue.GetExc(), caches, reverseLists),
			},
		}
	case livedebugger.Variant_VARIANT_TRACEBACK:
		dict["@CT"] = dictType
		stackTrace := make([]interface{}, len(v.GetCodeValues()))
		for i, codeValue := range v.GetCodeValues() {
			idx := i
			if reverseLists {
				idx = len(v.GetCodeValues()) - i - 1
			}
			stackTrace[idx] = map[string]interface{}{
				"filename": map[string]interface{}{"@value": caches.getStringFromCache(int(codeValue.GetFilenameIndexInCache()))},
				"module":   map[string]interface{}{"@value": caches.getStringFromCache(int(codeValue.GetModuleIndexInCache()))},
				"line":     map[string]interface{}{"@value": codeValue.GetLineno()},
				"function": map[string]interface{}{"@value": caches.getStringFromCache(int(codeValue.GetNameIndexInCache()))},
			}
		}
		dict["@value"] = stackTrace
		addAttributesToDict(dict, v, caches, reverseLists)
	case livedebugger.Variant_VARIANT_DYNAMIC:
		return map[string]interface{}{"@CT": dynamicType}
	case livedebugger.Variant_VARIANT_FORMATTED_MESSAGE:
		return map[string]interface{}{"@CT": stringType, "@value": caches.getStringFromCache(int(v.GetBytesIndexInCache()))}
	case livedebugger.Variant_VARIANT_MAX_DEPTH:
		return map[string]interface{}{"@CT": namespaceType}
	case livedebugger.Variant_VARIANT_LIVETAIL:
		return map[string]interface{}{"@CT": nullType, "@value": nil}
	default:
		dict["@CT"] = nullType
		dict["@value"] = nil
	}

	return dict
}

func addAttributesToDict(dict map[string]interface{}, v *livedebugger.Variant2, caches *variant2Caches, reverseLists bool) {
	if len(v.GetAttributeNamesInCache()) == 0 {
		return
	}

	attrs := make(map[string]interface{})
	for i, attrNameIndex := range v.GetAttributeNamesInCache() {
		if i < len(v.GetAttributeValues()) {
			attrName := caches.getStringFromCache(int(attrNameIndex))
			attrs[attrName] = variant2ToDict(v.GetAttributeValues()[i], caches, reverseLists)
		}
	}
	dict["@attributes"] = attrs
}

func reverseInterfaces(values []interface{}) {
	for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
		values[i], values[j] = values[j], values[i]
	}
}

func formatRookoutTimestamp(ts *livedebugger.Timestamp) string {
	if ts == nil {
		return ""
	}
	return time.Unix(ts.GetSeconds(), int64(ts.GetNanos())).UTC().Format("2006-01-02T15:04:05.000000Z")
}

func int64ToSafeJSNumber(num int64) interface{} {
	const jsMaxSafe = int64(1 << 53)
	if num < jsMaxSafe && num > -1*jsMaxSafe {
		return num
	}
	return fmt.Sprintf("%d", num)
}

const (
	stringType        = 1
	intType           = 2
	floatType         = 3
	nullType          = 5
	namespaceType     = 6
	boolType          = 7
	binaryType        = 8
	datetimeType      = 9
	setType           = 10
	listType          = 11
	dictType          = 12
	userObjectType    = 13
	unknownObjectType = 14
	enumType          = 15
	dynamicType       = 16
	complexType       = 17
)
