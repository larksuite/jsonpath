package main

import (
	"encoding/json"
	"fmt"

	"github.com/larksuite/jsonpath"
)

func main() {
	friend1 := &Dog{
		Name:  "Tony",
		Color: "White",
		Age:   10,
		IsMan: true,
	}
	friend2 := &Dog{
		Name:  "David",
		Color: "Yellow",
		Age:   9,
		IsMan: false,
	}
	tom := &Dog{
		Name:    "Tom",
		Color:   "Black",
		Age:     8,
		Friends: []*Dog{friend1, friend2},
	}

	var data interface{}
	marshal, _ := json.Marshal(tom)
	_ = json.Unmarshal(marshal, &data)

	//jp := "$.friends[?(@.name == 'Tony')].age"
	jp := "$.friends[name=Tony].name"

	// get
	value, err := jsonpath.JsonPathLookup(data, jp)
	fmt.Println(value, err)

	//set
	err = jsonpath.JsonPathSet(data, jp, "George")
	fmt.Println(data, err)

}

type Dog struct {
	Name    string `json:"name"`
	Color   string `json:"color"`
	Age     int    `json:"age"`
	IsMan   bool   `json:"isMan"`
	Friends []*Dog `json:"friends"`
}
