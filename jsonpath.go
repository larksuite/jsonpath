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

func Get(obj interface{}, jpath string) (interface{}, error) {
	c, err := Compile(jpath)
	if err != nil {
		return nil, err
	}
	return c.Lookup(obj)
}

func Set(obj interface{}, jpath string, val interface{}) error {
	c, err := Compile(jpath)
	if err != nil {
		return err
	}
	return c.Set(obj, val)
}

func Optimize(obj interface{}, path string) (string, error) {
	compiled, err := Compile(path)
	if err != nil {
		return "", err
	}
	path, err = compiled.revert(obj)
	if err != nil || path == "" {
		return "", err
	}
	return fmt.Sprintf("$%s", path), nil
}

type Compiled struct {
	path  string
	steps []step
}

type step struct {
	op   string
	key  string
	args interface{}
}

func MustCompile(jpath string) *Compiled {
	c, err := Compile(jpath)
	if err != nil {
		panic(err)
	}
	return c
}

func Compile(jpath string) (*Compiled, error) {
	tokens, err := tokenize(jpath)
	if err != nil {
		return nil, err
	}
	if tokens[0] != "@" && tokens[0] != "$" {
		return nil, fmt.Errorf("$ or @ should in front of path")
	}
	tokens = tokens[1:]
	res := Compiled{
		path:  jpath,
		steps: make([]step, len(tokens)),
	}
	for i, token := range tokens {
		op, key, args, err := parse_token(token)
		if err != nil {
			return nil, err
		}
		res.steps[i] = step{op, key, args}
	}
	return &res, nil
}

func (c *Compiled) String() string {
	return fmt.Sprintf("Compiled lookup: %s", c.path)
}

func (c *Compiled) revert(obj interface{}) (path string, err error) {
	path = ""
	for _, s := range c.steps {
		switch s.op {
		case "key":
			obj, err = getByKey(obj, s.key)
			if err != nil {
				return "", err
			}
			path += fmt.Sprintf(".%s", s.key)
		case "idx":
			if len(s.key) > 0 {
				obj, err = getByKey(obj, s.key)
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
				expr := getExpr(obj, s.key)
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
				obj, err = getByKey(obj, s.key)
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
			obj, err = getByKey(obj, s.key)
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

func getExpr(obj interface{}, key string) string {
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
		expr := getExpr(schema, "schema")
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

func (c *Compiled) Lookup(obj interface{}) (interface{}, error) {
	var err error
	for _, s := range c.steps {
		switch s.op {
		case "key":
			obj, err = getByKey(obj, s.key)
			if err != nil {
				return nil, err
			}
		case "idx":
			if len(s.key) > 0 {
				// no key `$[0].test`
				obj, err = getByKey(obj, s.key)
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
				obj, err = getByKey(obj, s.key)
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
			obj, err = getByKey(obj, s.key)
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
	if len(c.steps) < 1 {
		return fmt.Errorf("Need at least one levels to set value")
	}
	sub := Compiled{steps: c.steps[0 : len(c.steps)-1]}

	parent, err := sub.Lookup(obj)
	if err != nil {
		return err
	}

	lastStep := c.steps[len(c.steps)-1]
	switch lastStep.op {
	case "key":
		return set_key(parent, lastStep.key, val)
	case "idx":
		if len(lastStep.key) > 0 {
			// no key `$[0].test`
			parent, err = getByKey(parent, lastStep.key)
			if err != nil {
				return err
			}
		}
		if len(lastStep.args.([]int)) > 1 {
			return fmt.Errorf("cannot set multiple items")
		} else if len(lastStep.args.([]int)) == 1 {
			return set_idx(parent, lastStep.args.([]int)[0], val)
		} else {
			return fmt.Errorf("cannot set on empty slice")
		}
	default:
		return fmt.Errorf("Set must point to specific position")
	}
	return nil
}

func tokenize(query string) ([]string, error) {
	tokens := []string{}
	//	token_start := false
	//	token_end := false
	token := ""

	// fmt.Println("-------------------------------------------------- start")
	for idx, x := range query {
		token += string(x)
		// //fmt.Printf("idx: %d, x: %s, token: %s, tokens: %v\n", idx, string(x), token, tokens)
		if idx == 0 {
			if token == "$" || token == "@" {
				tokens = append(tokens, token[:])
				token = ""
				continue
			} else {
				return nil, fmt.Errorf("should start with '$'")
			}
		}
		if token == "." {
			continue
		} else if token == ".." {
			if tokens[len(tokens)-1] != "*" {
				tokens = append(tokens, "*")
			}
			token = "."
			continue
		} else {
			// fmt.Println("else: ", string(x), token)
			if strings.Contains(token, "[") {
				// fmt.Println(" contains [ ")
				if x == ']' && !strings.HasSuffix(token, "\\]") {
					if token[0] == '.' {
						tokens = append(tokens, token[1:])
					} else {
						tokens = append(tokens, token[:])
					}
					token = ""
					continue
				}
			} else {
				// fmt.Println(" doesn't contains [ ")
				if x == '.' {
					if token[0] == '.' {
						tokens = append(tokens, token[1:len(token)-1])
					} else {
						tokens = append(tokens, token[:len(token)-1])
					}
					token = "."
					continue
				}
			}
		}
	}
	if len(token) > 0 {
		if token[0] == '.' {
			token = token[1:]
			if token != "*" {
				tokens = append(tokens, token[:])
			} else if tokens[len(tokens)-1] != "*" {
				tokens = append(tokens, token[:])
			}
		} else {
			if token != "*" {
				tokens = append(tokens, token[:])
			} else if tokens[len(tokens)-1] != "*" {
				tokens = append(tokens, token[:])
			}
		}
	}
	// fmt.Println("finished tokens: ", tokens)
	// fmt.Println("================================================= done ")
	return tokens, nil
}

/*
 op: "root", "key", "idx", "range", "filter", "scan"
*/
func parse_token(token string) (op string, key string, args interface{}, err error) {
	if token == "$" {
		return "root", "$", nil, nil
	}
	if token == "*" {
		return "scan", "*", nil, nil
	}

	bracket_idx := strings.Index(token, "[")
	if bracket_idx < 0 {
		return "key", token, nil, nil
	} else {
		key = token[:bracket_idx]
		tail := token[bracket_idx:]
		if len(tail) < 3 {
			err = fmt.Errorf("len(tail) should >=3, %v", tail)
			return
		}
		tail = tail[1 : len(tail)-1]

		//fmt.Println(key, tail)
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

func filter_get_from_explicit_path(obj interface{}, path string) (interface{}, error) {
	steps, err := tokenize(path)
	//fmt.Println("f: steps: ", steps, err)
	//fmt.Println(path, steps)
	if err != nil {
		return nil, err
	}
	if steps[0] != "@" && steps[0] != "$" {
		return nil, fmt.Errorf("$ or @ should in front of path")
	}
	steps = steps[1:]
	xobj := obj
	//fmt.Println("f: xobj", xobj)
	for _, s := range steps {
		op, key, args, err := parse_token(s)
		// "key", "idx"
		switch op {
		case "key":
			xobj, err = getByKey(xobj, key)
			if err != nil {
				return nil, err
			}
		case "idx":
			if len(args.([]int)) != 1 {
				return nil, fmt.Errorf("don't support multiple index in filter")
			}
			xobj, err = getByKey(xobj, key)
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
				return nil, fmt.Errorf("key error: %s not found in object", key)
			}
			return val, nil
		}
		for _, kv := range reflect.ValueOf(obj).MapKeys() {
			//fmt.Println(kv.String())
			if kv.String() == key {
				return reflect.ValueOf(obj).MapIndex(kv).Interface(), nil
			}
		}
		return nil, fmt.Errorf("key error: %s not found in object", key)
	case reflect.Slice:
		// slice we should get from all objects in it.
		res := make([]interface{}, 0)
		for i := 0; i < reflect.ValueOf(obj).Len(); i++ {
			tmp, _ := getByIdx(obj, i)
			if v, err := getByKey(tmp, key); err == nil {
				res = append(res, v)
			}
		}
		return res, nil
	default:
		return nil, fmt.Errorf("object is not map")
	}
}

func set_key(obj interface{}, key string, value interface{}) error {
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
			err := set_key(v.Index(i).Interface(), key, value)
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
				return nil, fmt.Errorf("index out of range: len: %v, idx: %v", length, idx)
			}
			return reflect.ValueOf(obj).Index(idx).Interface(), nil
		} else {
			// < 0
			_idx := length + idx
			if _idx < 0 {
				return nil, fmt.Errorf("index out of range: len: %v, idx: %v", length, idx)
			}
			return reflect.ValueOf(obj).Index(_idx).Interface(), nil
		}
	default:
		return nil, fmt.Errorf("object is not Slice")
	}
}

func get_match(obj interface{}, field, target string) (matchObj interface{}, err error) {
	v := reflect.ValueOf(obj)
	switch v.Kind() {
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			element := v.Index(i).Interface()
			matchObj, err = get_match(element, field, target)
			if matchObj != nil || err != nil {
				return
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			if iter.Key().String() == field && fmt.Sprint(iter.Value().Interface()) == target {
				return v.Interface(), nil
			}
		}
	default:
		return nil, fmt.Errorf("object is not map")
	}

	return nil, nil
}

func set_idx(obj interface{}, idx int, val interface{}) error {
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
			return nil, fmt.Errorf("index [from] out of range: len: %v, from: %v", length, frm)
		}
		if _to < 0 || _to > length {
			return nil, fmt.Errorf("index [to] out of range: len: %v, to: %v", length, to)
		}
		//fmt.Println("_frm, _to: ", _frm, _to)
		res_v := reflect.ValueOf(obj).Slice(_frm, _to)
		return res_v.Interface(), nil
	default:
		return nil, fmt.Errorf("object is not Slice")
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
		return filter_get_from_explicit_path(obj, path)
	} else if strings.HasPrefix(path, "$.") {
		return filter_get_from_explicit_path(root, path)
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
