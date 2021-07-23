package main

import (
	"encoding/json"
	"fmt"

	"github.com/numary/machine/script/compiler"
	"github.com/numary/machine/vm"
)

func main() {
	p, err := compiler.Compile(`
		send [GEM 15] (
			source = @world
			destination = {
				2/3 to @a
				remaining to @b
				remaining to @c
			}
		)`)

	if err != nil {
		fmt.Println(err)
		panic("compile error")
	}

	fmt.Println(p)

	machine := vm.NewMachine(p)

	var vars map[string]json.RawMessage
	json.Unmarshal([]byte(`{
		"value": {
			"asset": "GEM",
			"amount": 45
		},
		"commission": "12.5%"
	}`), &vars)
	err = machine.SetVarsFromJSON(vars)

	if err != nil {
		panic(err)
	}
	err = machine.SetBalances(map[string]map[string]uint64{
		"users:001": {
			"GEM": 2500,
		},
	})
	if err != nil {
		panic(err)
	}
	exit_code, err := machine.Execute()
	if err != nil {
		panic(err)
	}
	fmt.Println("EXIT_CODE:", exit_code)
	fmt.Println(machine.Postings)
}
