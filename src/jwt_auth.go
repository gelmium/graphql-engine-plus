package main

import (
	"fmt"

	"github.com/golang-jwt/jwt/v5"
	jsoniter "github.com/json-iterator/go"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

//	type _XHasuraUserId struct {
//		Path    string `json:"path"`
//		Default string `json:"default"`
//	}
//
//	type _ClaimsMap struct {
//		XHasuraAllowedRoles interface{}    `json:"x-hasura-allowed-roles"`
//		XHasuraDefaultRole  interface{}    `json:"x-hasura-default-role"`
//		XHasuraUserId       _XHasuraUserId `json:"x-hasura-user-id"`
//	}
type _Header struct {
	Type string `json:"type"`
}
type JwtAuthParser struct {
	Type   string  `json:"type"`
	Key    string  `json:"key"`
	Header _Header `json:"header"`
	// ClaimsNamespace     string     `json:"claims_namespace"`
	// ClaimsNamespacePath string     `json:"claims_namespace_path"`
	// ClaimsFormat        string     `json:"claims_format"`
	// ClaimsMap           _ClaimsMap `json:"claims_map"`
}

func ReadHasuraGraphqlJwtSecretConfig(jwtConfigString string) JwtAuthParser {
	jwtAuthParserConfig := JwtAuthParser{}
	json.Unmarshal([]byte(jwtConfigString), &jwtAuthParserConfig)
	return jwtAuthParserConfig
}

type Result struct {
}

func (jwtAuthParserConfig JwtAuthParser) ParseJwt(tokenString string) (jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		// validate the alg is what we expect:
		if token.Method.Alg() != jwtAuthParserConfig.Type {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		// hmacSampleSecret is a []byte containing your secret, e.g. []byte("my_secret_key")
		return jwtAuthParserConfig.Key, nil
	})
	claims, ok := token.Claims.(jwt.MapClaims)
	if ok {
		// remove timestamp from claims
		delete(claims, "exp")
		delete(claims, "nbf")
		delete(claims, "iat")
		// remove jwt id from claims
		delete(claims, "jti")
		return claims, nil
	} else {
		return nil, err
	}
}
