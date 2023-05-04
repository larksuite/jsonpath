package jsonpath

import (
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

var ErrGetFromNullObj = errors.New("get attribute from null object")
var NotJSON = errors.New("object is not json")
var NotMap = errors.New("object is not map")
var NotSlice = errors.New("object is not slice")
var IsNull = errors.New("object is nil")

func Get(obj interface{}, path string) (*Result, error) {
	c, err := Compile(path)
	if err != nil {
		return nil, err
	}
	value, isArray, err := c.Lookup(obj)
	if err != nil {
		return nil, err
	}
	return &Result{
		value:   value,
		isArray: isArray,
	}, nil
}

func Set(obj interface{}, jpath string, val interface{}) error {
	c, err := Compile(jpath)
	if err != nil {
		return err
	}
	return c.Set(obj, val)
}

func TranslatePath(obj interface{}, path string) (string, error) {
	compiled, err := Compile(path)
	if err != nil {
		return "", err
	}
	path, _, err = compiled.decompile(obj)
	if err != nil || path == "" {
		return "", err
	}
	return fmt.Sprintf("$%s", path), nil
}

type Compiled struct {
	path       string
	operations []operation
	step       int
}

type operation struct {
	op   string
	key  string
	args interface{}
}

type Result struct {
	value   interface{}
	isArray bool
}

func (r *Result) Value() interface{} {
	return r.value
}

// First Provides the first item of an array
func (r *Result) First() interface{} {
	if r.isArray && reflect.TypeOf(r.value).Kind() == reflect.Slice {
		v := reflect.ValueOf(r.value)
		if reflect.ValueOf(r.value).Len() > 0 {
			return v.Index(0).Interface()
		} else {
			return nil
		}
	}

	return r.value
}

func MustCompile(jpath string) *Compiled {
	c, err := Compile(jpath)
	if err != nil {
		panic(err)
	}
	return c
}

func Compile(path string) (*Compiled, error) {
	fragments, err := parse(path)
	if err != nil {
		return nil, err
	}
	if fragments[0] != "@" && fragments[0] != "$" {
		return nil, fmt.Errorf("path should start with '$' or '@'")
	}
	fragments = fragments[1:]
	res := Compiled{
		path:       path,
		operations: make([]operation, len(fragments)),
		step:       0,
	}
	for i, fragment := range fragments {
		op, key, args, err := parseFragment(fragment)
		if err != nil {
			return nil, err
		}
		res.operations[i] = operation{op, key, args}
	}
	return &res, nil
}

func (c *Compiled) next() *Compiled {
	if c.step == len(c.operations)-1 {
		return nil
	}
	c.step++
	return c
}

func (c *Compiled) String() string {
	return fmt.Sprintf("Compiled lookup: %s", c.path)
}

func (c *Compiled) _decompile(obj interface{}) (path string, err error) {
	path = ""
	for _, s := range c.operations {
		switch s.op {
		case "key":
			obj, err = _getByKey(obj, s.key)
			if err != nil {
				return "", err
			}
			path += fmt.Sprintf(".%s", s.key)
		case "idx":
			if len(s.key) > 0 {
				obj, err = _getByKey(obj, s.key)
				if err != nil {
					return "", err
				}
			}
			idxs := s.args.([]int)
			ss := make([]string, 0, len(idxs))
			if len(idxs) > 1 {
				res := make([]interface{}, 0)
				for _, i := range idxs {
					tmp, err := getByIdx(obj, i)
					if err != nil {
						return "", err
					}
					res = append(res, tmp)
					ss = append(ss, strconv.Itoa(i))
				}
				obj = res
				path += fmt.Sprintf(".%s[%s]", s.key, strings.Join(ss, ","))
			} else if len(idxs) == 1 {
				obj, err = getByIdx(obj, idxs[0])
				if err != nil {
					return "", err
				}
				expr := getFilterExpr(obj, s.key)
				if expr != "" {
					path += fmt.Sprintf(".%s[?(%s)]", s.key, expr)
				} else {
					path += fmt.Sprintf(".%s[%d]", s.key, idxs[0])
				}
			} else {
				return "", fmt.Errorf("cannot index on empty slice")
			}
		case "range":
			if len(s.key) > 0 {
				obj, err = _getByKey(obj, s.key)
				if err != nil {
					return "", err
				}
			}
			if args, ok := s.args.([2]interface{}); ok == true {
				obj, err = getByRange(obj, args[0], args[1])
				if err != nil {
					return "", err
				}
				from := ""
				to := ""
				if args[0] != nil {
					from = fmt.Sprintf("%v", args[0])
				}
				if args[1] != nil {
					to = fmt.Sprintf("%v", args[1])
				}
				if from == "" && to == "" {
					path += fmt.Sprintf(".%s[*]", s.key)
				} else {
					path += fmt.Sprintf(".%s[%s:%s]", s.key, from, to)
				}
			} else {
				return "", fmt.Errorf("range args length should be 2")
			}
		case "filter":
			obj, err = _getByKey(obj, s.key)
			if err != nil {
				return "", err
			}
			obj, err = getFiltered(obj, obj, s.args.(string))
			if err != nil {
				return "", err
			}
			path += fmt.Sprintf(".%s[%v]", s.key, s.args)
		default:
			return "", fmt.Errorf("expression don't support in filter")
		}
	}

	return path, nil
}

func (c *Compiled) decompile(obj interface{}) (path string, isArray bool, err error) {
	if reflect.TypeOf(obj) == nil {
		err = IsNull
		return
	}
	switch reflect.TypeOf(obj).Kind() {
	case reflect.Slice:
		for i := 0; i < reflect.ValueOf(obj).Len(); i++ {
			item := reflect.ValueOf(obj).Index(i).Interface()
			path, isArray, err = c.decompile(item)
			if err != nil {
				continue
			}
		}
		isArray = true
		return
	case reflect.Map:
		operation := c.operations[c.step]
		switch operation.op {
		case "key":
			obj, err = getByKey(obj, operation.key)
			if err != nil {
				return
			}
			path = fmt.Sprintf(".%s", operation.key)
		case "idx":
			if len(operation.key) > 0 {
				// no key `$[0].test`
				obj, err = getByKey(obj, operation.key)
				if err != nil {
					return
				}
			}

			idxs := operation.args.([]int)
			ss := make([]string, 0, len(idxs))
			if len(idxs) > 1 {
				arr := make([]interface{}, 0, len(idxs))
				for _, idx := range idxs {
					var item interface{}
					item, err = getByIdx(obj, idx)
					if err != nil {
						return
					}
					arr = append(arr, item)
					ss = append(ss, strconv.Itoa(idx))
				}
				obj = arr
				isArray = true
				path = fmt.Sprintf(".%s[%s]", operation.key, strings.Join(ss, ","))
			} else if len(idxs) == 1 {
				obj, err = getByIdx(obj, idxs[0])
				if err != nil {
					return
				}
				expr := getFilterExpr(obj, operation.key)
				if expr != "" {
					path = fmt.Sprintf(".%s[?(%s)]", operation.key, expr)
				} else {
					path = fmt.Sprintf(".%s[%d]", operation.key, idxs[0])
				}
			} else {
				err = fmt.Errorf("cannot index on empty slice")
				return
			}
		case "range":
			if len(operation.key) > 0 {
				obj, err = getByKey(obj, operation.key)
				if err != nil {
					return
				}
			}
			if args, ok := operation.args.([2]interface{}); ok == true {
				obj, err = getByRange(obj, args[0], args[1])
				if err != nil {
					return
				}
				isArray = true
				from := ""
				to := ""
				if args[0] != nil {
					from = fmt.Sprintf("%v", args[0])
				}
				if args[1] != nil {
					to = fmt.Sprintf("%v", args[1])
				}
				if from == "" && to == "" {
					path = fmt.Sprintf(".%s[*]", operation.key)
				} else {
					path = fmt.Sprintf(".%s[%s:%s]", operation.key, from, to)
				}
			} else {
				err = fmt.Errorf("range args length should be 2")
				return
			}
		case "filter":
			obj, err = getByKey(obj, operation.key)
			if err != nil {
				return
			}
			obj, err = getFiltered(obj, obj, operation.args.(string))
			if err != nil {
				return
			}
			isArray = true
			path = fmt.Sprintf(".%s[?(%v)]", operation.key, operation.args)
		default:
			err = fmt.Errorf("expression don't support in filter")
			return
		}
	default:
		err = NotJSON
		return
	}

	next := c.next()
	if next == nil {
		return
	}

	suffix, isArray, err := next.decompile(obj)
	return path + suffix, isArray, err
}

func (c *Compiled) Lookup(obj interface{}) (res interface{}, isArray bool, err error) {
	if obj == nil {
		return
	}
	switch reflect.TypeOf(obj).Kind() {
	case reflect.Slice:
		arr := make([]interface{}, 0)
		for i := 0; i < reflect.ValueOf(obj).Len(); i++ {
			item := reflect.ValueOf(obj).Index(i).Interface()
			var value interface{}
			value, isArray, err = c.Lookup(item)
			if err != nil {
				continue
			}
			if isArray && reflect.TypeOf(value).Kind() == reflect.Slice {
				v := reflect.ValueOf(value)
				for j := 0; j < v.Len(); j++ {
					arr = append(arr, v.Index(j).Interface())
				}
			} else {
				arr = append(arr, value)
			}
		}
		res = arr
		isArray = true
		return
	case reflect.Map:
		operation := c.operations[c.step]
		switch operation.op {
		case "key":
			obj, err = getByKey(obj, operation.key)
			if err != nil {
				return
			}
		case "idx":
			if len(operation.key) > 0 {
				obj, err = getByKey(obj, operation.key)
				if err != nil {
					return
				}
			}

			idxs := operation.args.([]int)
			if len(idxs) > 1 {
				arr := make([]interface{}, 0, len(idxs))
				for _, idx := range idxs {
					var item interface{}
					item, err = getByIdx(obj, idx)
					if err != nil {
						return
					}
					arr = append(arr, item)
				}
				obj = arr
				isArray = true
			} else if len(idxs) == 1 {
				obj, err = getByIdx(obj, idxs[0])
				if err != nil {
					return
				}
			} else {
				err = fmt.Errorf("cannot index on empty slice")
				return
			}
		case "range":
			if len(operation.key) > 0 {
				obj, err = getByKey(obj, operation.key)
				if err != nil {
					return
				}
			}
			if args, ok := operation.args.([2]interface{}); ok == true {
				obj, err = getByRange(obj, args[0], args[1])
				if err != nil {
					return
				}
				isArray = true
			} else {
				err = fmt.Errorf("range args length should be 2")
				return
			}
		case "filter":
			obj, err = getByKey(obj, operation.key)
			if err != nil {
				return
			}
			obj, err = getFiltered(obj, obj, operation.args.(string))
			if err != nil {
				return
			}
			isArray = true
		default:
			err = fmt.Errorf("expression don't support in filter")
			return
		}
	default:
		err = NotJSON
		return
	}

	next := c.next()
	if next == nil {
		res = obj
		return
	}
	return next.Lookup(obj)
}

func (c *Compiled) _Lookup(obj interface{}) (interface{}, error) {
	var err error
	for _, s := range c.operations {
		switch s.op {
		case "key":
			obj, err = _getByKey(obj, s.key)
			if err != nil {
				return nil, err
			}
		case "idx":
			if len(s.key) > 0 {
				// no key `$[0].test`
				obj, err = _getByKey(obj, s.key)
				if err != nil {
					return nil, err
				}
			}

			if len(s.args.([]int)) > 1 {
				res := make([]interface{}, 0)
				for _, x := range s.args.([]int) {
					tmp, err := getByIdx(obj, x)
					if err != nil {
						return nil, err
					}
					res = append(res, tmp)
				}
				obj = res
			} else if len(s.args.([]int)) == 1 {
				obj, err = getByIdx(obj, s.args.([]int)[0])
				if err != nil {
					return nil, err
				}
			} else {
				return nil, fmt.Errorf("cannot index on empty slice")
			}
		case "range":
			if len(s.key) > 0 {
				// no key `$[:1].test`
				obj, err = _getByKey(obj, s.key)
				if err != nil {
					return nil, err
				}
			}
			if args, ok := s.args.([2]interface{}); ok == true {
				obj, err = getByRange(obj, args[0], args[1])
				if err != nil {
					return nil, err
				}
			} else {
				return nil, fmt.Errorf("range args length should be 2")
			}
		case "filter":
			obj, err = _getByKey(obj, s.key)
			if err != nil {
				return nil, err
			}
			obj, err = getFiltered(obj, obj, s.args.(string))
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("expression don't support in filter")
		}
	}
	return obj, nil
}

func (c *Compiled) Set(obj interface{}, val interface{}) error {
	if len(c.operations) < 1 {
		return fmt.Errorf("need at least one levels to set value")
	}
	sub := Compiled{operations: c.operations[0 : len(c.operations)-1]}

	parent, err := sub._Lookup(obj)
	if err != nil {
		return err
	}

	lastStep := c.operations[len(c.operations)-1]
	switch lastStep.op {
	case "key":
		return setByKey(parent, lastStep.key, val)
	case "idx":
		if len(lastStep.key) > 0 {
			// no key `$[0].test`
			parent, err = _getByKey(parent, lastStep.key)
			if err != nil {
				return err
			}
		}
		if len(lastStep.args.([]int)) > 1 {
			return fmt.Errorf("cannot set multiple items")
		} else if len(lastStep.args.([]int)) == 1 {
			return setByIdx(parent, lastStep.args.([]int)[0], val)
		} else {
			return fmt.Errorf("cannot set on empty slice")
		}
	default:
		return fmt.Errorf("set must point to specific position")
	}
	return nil
}

func parse(query string) ([]string, error) {
	fragments := make([]string, 0)
	fragment := ""

	for idx, x := range query {
		fragment += string(x)
		if idx == 0 {
			if fragment == "$" || fragment == "@" {
				fragments = append(fragments, fragment[:])
				fragment = ""
				continue
			} else {
				return nil, fmt.Errorf("should start with '$'")
			}
		}
		if fragment == "." {
			continue
		} else if fragment == ".." {
			if fragments[len(fragments)-1] != "*" {
				fragments = append(fragments, "*")
			}
			fragment = "."
			continue
		} else {
			if strings.Contains(fragment, "[") {
				if x == ']' && !strings.HasSuffix(fragment, "\\]") {
					if fragment[0] == '.' {
						fragments = append(fragments, fragment[1:])
					} else {
						fragments = append(fragments, fragment[:])
					}
					fragment = ""
					continue
				}
			} else {
				if x == '.' {
					if fragment[0] == '.' {
						fragments = append(fragments, fragment[1:len(fragment)-1])
					} else {
						fragments = append(fragments, fragment[:len(fragment)-1])
					}
					fragment = "."
					continue
				}
			}
		}
	}
	if len(fragment) > 0 {
		if fragment[0] == '.' {
			fragment = fragment[1:]
			if fragment != "*" {
				fragments = append(fragments, fragment[:])
			} else if fragments[len(fragments)-1] != "*" {
				fragments = append(fragments, fragment[:])
			}
		} else {
			if fragment != "*" {
				fragments = append(fragments, fragment[:])
			} else if fragments[len(fragments)-1] != "*" {
				fragments = append(fragments, fragment[:])
			}
		}
	}

	return fragments, nil
}

/*
 op: "root", "key", "idx", "range", "filter", "scan"
*/
func parseFragment(token string) (op string, key string, args interface{}, err error) {
	if token == "$" {
		return "root", "$", nil, nil
	}
	if token == "*" {
		return "scan", "*", nil, nil
	}

	bracketIdx := strings.Index(token, "[")
	if bracketIdx < 0 {
		return "key", token, nil, nil
	} else {
		key = token[:bracketIdx]
		tail := token[bracketIdx:]
		if len(tail) < 3 {
			err = fmt.Errorf("len(tail) should >=3, %v", tail)
			return
		}
		tail = tail[1 : len(tail)-1]

		if strings.Contains(tail, "?") {
			// filter -------------------------------------------------
			op = "filter"
			if strings.HasPrefix(tail, "?(") && strings.HasSuffix(tail, ")") {
				args = strings.Trim(tail[2:len(tail)-1], " ")
			}
			return
		} else if strings.Contains(tail, ":") {
			// range ----------------------------------------------
			op = "range"
			tails := strings.Split(tail, ":")
			if len(tails) != 2 {
				err = fmt.Errorf("only support one range(from, to): %v", tails)
				return
			}
			var frm interface{}
			var to interface{}
			if frm, err = strconv.Atoi(strings.Trim(tails[0], " ")); err != nil {
				if strings.Trim(tails[0], " ") == "" {
					err = nil
				}
				frm = nil
			}
			if to, err = strconv.Atoi(strings.Trim(tails[1], " ")); err != nil {
				if strings.Trim(tails[1], " ") == "" {
					err = nil
				}
				to = nil
			}
			args = [2]interface{}{frm, to}
			return
		} else if tail == "*" {
			op = "range"
			args = [2]interface{}{nil, nil}
			return
		} else {
			// idx ------------------------------------------------
			op = "idx"
			res := []int{}
			for _, x := range strings.Split(tail, ",") {
				if i, err := strconv.Atoi(strings.Trim(x, " ")); err == nil {
					res = append(res, i)
				} else {
					return "", "", nil, err
				}
			}
			args = res
		}
	}
	return op, key, args, nil
}

func filterGetFromExplicitPath(obj interface{}, path string) (interface{}, error) {
	steps, err := parse(path)
	if err != nil {
		return nil, err
	}
	if steps[0] != "@" && steps[0] != "$" {
		return nil, fmt.Errorf("$ or @ should in front of path")
	}
	steps = steps[1:]
	xobj := obj
	for _, s := range steps {
		op, key, args, err := parseFragment(s)
		// "key", "idx"
		switch op {
		case "key":
			xobj, err = _getByKey(xobj, key)
			if err != nil {
				return nil, err
			}
		case "idx":
			if len(args.([]int)) != 1 {
				return nil, fmt.Errorf("don't support multiple index in filter")
			}
			xobj, err = _getByKey(xobj, key)
			if err != nil {
				return nil, err
			}
			xobj, err = getByIdx(xobj, args.([]int)[0])
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("expression don't support in filter")
		}
	}
	return xobj, nil
}

func getByKey(obj interface{}, key string) (interface{}, error) {
	if reflect.TypeOf(obj).Kind() != reflect.Map {
		return nil, NotMap
	}
	if json, ok := obj.(map[string]interface{}); ok {
		value, exists := json[key]
		if !exists {
			return nil, fmt.Errorf("no match: %s not found in object", key)
		}
		return value, nil
	}
	for _, kv := range reflect.ValueOf(obj).MapKeys() {
		if kv.String() == key {
			return reflect.ValueOf(obj).MapIndex(kv).Interface(), nil
		}
	}
	return nil, fmt.Errorf("no match: %s not found in object", key)
}

func _getByKey(obj interface{}, key string) (interface{}, error) {
	if reflect.TypeOf(obj) == nil {
		return nil, ErrGetFromNullObj
	}
	switch reflect.TypeOf(obj).Kind() {
	case reflect.Map:
		// if obj came from stdlib json, its highly likely to be a map[string]interface{}
		// in which case we can save having to iterate the map keys to work out if the
		// key exists
		if jsonMap, ok := obj.(map[string]interface{}); ok {
			val, exists := jsonMap[key]
			if !exists {
				return nil, fmt.Errorf("no match: %s not found in object", key)
			}
			return val, nil
		}
		for _, kv := range reflect.ValueOf(obj).MapKeys() {
			if kv.String() == key {
				return reflect.ValueOf(obj).MapIndex(kv).Interface(), nil
			}
		}
		return nil, fmt.Errorf("no match: %s not found in object", key)
	case reflect.Slice:
		// slice we should get from all objects in it.
		res := make([]interface{}, 0)
		for i := 0; i < reflect.ValueOf(obj).Len(); i++ {
			tmp, _ := getByIdx(obj, i)
			if v, err := _getByKey(tmp, key); err == nil {
				res = append(res, v)
			}
		}
		return res, nil
	default:
		return nil, fmt.Errorf("object is not map")
	}
}

func setByKey(obj interface{}, key string, value interface{}) error {
	if reflect.TypeOf(obj) == nil {
		return ErrGetFromNullObj
	}
	switch reflect.TypeOf(obj).Kind() {
	case reflect.Map:
		// if obj came from stdlib json, its highly likely to be a map[string]interface{}
		// in which case we can save having to iterate the map keys to work out if the
		// key exists
		if jsonMap, ok := obj.(map[string]interface{}); ok {
			jsonMap[key] = value
			return nil
		}
		return fmt.Errorf("Unable to place key in map")
	case reflect.Slice:
		v := reflect.ValueOf(obj)
		for i := 0; i < v.Len(); i++ {
			err := setByKey(v.Index(i).Interface(), key, value)
			if err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("object is not map")
	}
}

func getByIdx(obj interface{}, idx int) (interface{}, error) {
	switch reflect.TypeOf(obj).Kind() {
	case reflect.Slice:
		length := reflect.ValueOf(obj).Len()
		if idx >= 0 {
			if idx >= length {
				return nil, fmt.Errorf("no match: index out of range: len: %v, idx: %v", length, idx)
			}
			return reflect.ValueOf(obj).Index(idx).Interface(), nil
		} else {
			_idx := length + idx
			if _idx < 0 {
				return nil, fmt.Errorf("no match: index out of range: len: %v, idx: %v", length, idx)
			}
			return reflect.ValueOf(obj).Index(_idx).Interface(), nil
		}
	default:
		return nil, NotSlice
	}
}

func setByIdx(obj interface{}, idx int, val interface{}) error {
	switch reflect.TypeOf(obj).Kind() {
	case reflect.Slice:
		length := reflect.ValueOf(obj).Len()
		if idx >= 0 {
			if idx >= length {
				return fmt.Errorf("index out of range: len: %v, idx: %v", length, idx)
			}
			reflect.ValueOf(obj).Index(idx).Set(reflect.ValueOf(val))
			return nil
		} else {
			// < 0
			_idx := length + idx
			if _idx < 0 {
				return fmt.Errorf("index out of range: len: %v, idx: %v", length, idx)
			}
			reflect.ValueOf(obj).Index(idx).Set(reflect.ValueOf(val))
			return nil
		}
	default:
		return fmt.Errorf("object is not Slice: %s", reflect.TypeOf(obj).Kind())
	}
}

func getByRange(obj, frm, to interface{}) (interface{}, error) {
	switch reflect.TypeOf(obj).Kind() {
	case reflect.Slice:
		length := reflect.ValueOf(obj).Len()
		_frm := 0
		_to := length
		if frm == nil {
			frm = 0
		}
		if to == nil {
			to = length - 1
		}
		if fv, ok := frm.(int); ok == true {
			if fv < 0 {
				_frm = length + fv
			} else {
				_frm = fv
			}
		}
		if tv, ok := to.(int); ok == true {
			if tv < 0 {
				_to = length + tv + 1
			} else {
				_to = tv + 1
			}
		}
		if _frm < 0 || _frm >= length {
			return nil, fmt.Errorf("no match: index [from] out of range: len: %v, from: %v", length, frm)
		}
		if _to < 0 || _to > length {
			return nil, fmt.Errorf("no match: index [to] out of range: len: %v, to: %v", length, to)
		}
		arr := reflect.ValueOf(obj).Slice(_frm, _to)
		return arr.Interface(), nil
	default:
		return nil, NotSlice
	}
}

func compileRegexp(rule string) (*regexp.Regexp, error) {
	runes := []rune(rule)
	if len(runes) <= 2 {
		return nil, errors.New("empty rule")
	}

	if runes[0] != '/' || runes[len(runes)-1] != '/' {
		return nil, errors.New("invalid syntax. should be in `/pattern/` form")
	}
	runes = runes[1 : len(runes)-1]
	return regexp.Compile(string(runes))
}

func getFiltered(obj, root interface{}, filter string) ([]interface{}, error) {
	res := make([]interface{}, 0)
	expressions, err := parseFilter(filter)
	if err != nil || len(expressions) == 0 {
		return res, err
	}

	switch reflect.TypeOf(obj).Kind() {
	case reflect.Slice:
		for i := 0; i < reflect.ValueOf(obj).Len(); i++ {
			tmp := reflect.ValueOf(obj).Index(i).Interface()
			match := true
			for _, expr := range expressions {
				ok, _ := evalFilter(tmp, root, expr.lp, expr.op, expr.rp)
				match = match && ok
				if !match {
					break
				}
			}
			if match {
				res = append(res, tmp)
			}
		}

		return res, nil
	case reflect.Map:
		for _, kv := range reflect.ValueOf(obj).MapKeys() {
			tmp := reflect.ValueOf(obj).MapIndex(kv).Interface()
			match := true
			for _, expr := range expressions {
				ok, _ := evalFilter(tmp, root, expr.lp, expr.op, expr.rp)
				match = match && ok
				if !match {
					break
				}
			}
			if match {
				res = append(res, tmp)
			}
		}
	default:
		return nil, fmt.Errorf("don't support filter on this type: %v", reflect.TypeOf(obj).Kind())
	}

	return res, nil
}

type FilterExpression struct {
	lp string
	op string
	rp string
}

// @.isbn                 => @.isbn, exists, nil
// @.price < 10           => @.price, <, 10
// @.price <= $.expensive => @.price, <=, $.expensive
// @.author =~ /.*REES/i  => @.author, match, /.*REES/i
func parseFilter(filter string) (expressions []*FilterExpression, err error) {
	subs := strings.Split(filter, "&&")
	expressions = make([]*FilterExpression, 0, len(subs))
	for _, sub := range subs {
		sub = strings.TrimSpace(sub)
		tmp, lp, op, rp := "", "", "", ""

		stage := 0
		strEmbrace := false
		for idx, c := range sub {
			switch c {
			case '\'':
				if strEmbrace == false {
					strEmbrace = true
				} else {
					switch stage {
					case 0:
						lp = tmp
					case 1:
						op = tmp
					case 2:
						rp = tmp
					}
					tmp = ""
				}
			case ' ':
				if strEmbrace == true {
					tmp += string(c)
					continue
				}
				switch stage {
				case 0:
					lp = tmp
				case 1:
					op = tmp
				case 2:
					rp = tmp
				}
				tmp = ""

				stage += 1
				if stage > 2 {
					err = errors.New(fmt.Sprintf("invalid char at %d: `%c`", idx, c))
					return
				}
			default:
				tmp += string(c)
			}
		}
		if tmp != "" {
			switch stage {
			case 0:
				lp = tmp
				op = "exists"
			case 1:
				op = tmp
			case 2:
				rp = tmp
			}
			tmp = ""
		}

		expr := &FilterExpression{
			lp: lp,
			op: op,
			rp: rp,
		}
		expressions = append(expressions, expr)
	}

	return
}

func parse_filter_v1(filter string) (lp string, op string, rp string, err error) {
	tmp := ""
	istoken := false
	for _, c := range filter {
		if istoken == false && c != ' ' {
			istoken = true
		}
		if istoken == true && c == ' ' {
			istoken = false
		}
		if istoken == true {
			tmp += string(c)
		}
		if istoken == false && tmp != "" {
			if lp == "" {
				lp = tmp[:]
				tmp = ""
			} else if op == "" {
				op = tmp[:]
				tmp = ""
			} else if rp == "" {
				rp = tmp[:]
				tmp = ""
			}
		}
	}
	if tmp != "" && lp == "" && op == "" && rp == "" {
		lp = tmp[:]
		op = "exists"
		rp = ""
		err = nil
		return
	} else if tmp != "" && rp == "" {
		rp = tmp[:]
		tmp = ""
	}
	return lp, op, rp, err
}

func evalRegexp(obj, root interface{}, lp string, pat *regexp.Regexp) (res bool, err error) {
	if pat == nil {
		return false, errors.New("nil pat")
	}
	lp_v, err := getByPath(obj, root, lp)
	if err != nil {
		return false, err
	}
	switch v := lp_v.(type) {
	case string:
		return pat.MatchString(v), nil
	default:
		return false, errors.New("only string can match with regular expression")
	}
}

func getByPath(obj, root interface{}, path string) (interface{}, error) {
	var v interface{}
	if strings.HasPrefix(path, "@.") {
		return filterGetFromExplicitPath(obj, path)
	} else if strings.HasPrefix(path, "$.") {
		return filterGetFromExplicitPath(root, path)
	} else {
		v = path
	}
	return v, nil
}

func evalFilter(obj, root interface{}, lp, op, rp string) (bool, error) {
	left, err := getByPath(obj, root, lp)
	if err != nil {
		return false, err
	}

	switch op {
	case "exists":
		return left != nil, nil
	case "=~":
		reg, err := compileRegexp(rp)
		if err != nil {
			return false, err
		}
		return evalRegexp(obj, root, lp, reg)
	default:
		right, err := getByPath(obj, root, rp)
		if err != nil {
			return false, err
		}

		return compare(left, right, op)
	}
}

func isNumber(o interface{}) bool {
	switch v := o.(type) {
	case int, int8, int16, int32, int64:
		return true
	case uint, uint8, uint16, uint32, uint64:
		return true
	case float32, float64:
		return true
	case string:
		_, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return true
		} else {
			return false
		}
	}
	return false
}

func compare(obj1, obj2 interface{}, op string) (bool, error) {
	switch op {
	case "<", "<=", "==", ">=", ">":
	default:
		return false, fmt.Errorf("op should only be <, <=, ==, >= and >")
	}

	var exp string
	if isNumber(obj1) && isNumber(obj2) {
		exp = fmt.Sprintf(`%v %s %v`, obj1, op, obj2)
	} else {
		exp = fmt.Sprintf(`"%v" %s "%v"`, obj1, op, obj2)
	}
	//fmt.Println("exp: ", exp)
	fset := token.NewFileSet()
	res, err := types.Eval(fset, nil, 0, exp)
	if err != nil {
		return false, err
	}
	if res.IsValue() == false || (res.Value.String() != "false" && res.Value.String() != "true") {
		return false, fmt.Errorf("result should only be true or false")
	}
	if res.Value.String() == "true" {
		return true, nil
	}

	return false, nil
}

func getFilterExpr(obj interface{}, key string) string {
	if reflect.TypeOf(obj).Kind() != reflect.Map {
		return ""
	}
	jsonMap, ok := obj.(map[string]interface{})
	if !ok {
		return ""
	}
	switch key {
	case "tips":
		level, ok := jsonMap["tipLevel"]
		if !ok {
			return ""
		}
		return fmt.Sprintf("@.tipLevel == '%v'", level)
	case "parameters":
		in, ok1 := jsonMap["in"]
		schema, ok2 := jsonMap["schema"]
		if !ok1 || !ok2 {
			return ""
		}
		expr := getFilterExpr(schema, "schema")
		if expr == "" {
			return ""
		}
		return fmt.Sprintf("@.in == '%v' && '%s'", in, expr)
	case "schema":
		name, ok := jsonMap["name"]
		if !ok {
			return ""
		}
		return fmt.Sprintf("@.schema.name == '%v'", name)
	case "properties", "options":
		name, ok := jsonMap["name"]
		if !ok {
			return ""
		}
		return fmt.Sprintf("@.name == '%v'", name)
	case "errorCodeMapping":
		code, ok := jsonMap["errorCode"]
		if !ok {
			return ""
		}
		return fmt.Sprintf("@.errorCode == %v", code)
	default:
		return ""
	}
}
