package main

import (
	"regexp"

	"github.com/gofiber/fiber/v2"
)

type GraphQLRequest struct {
	Query         string                 `json:"query"`
	Variables     map[string]interface{} `json:"variables"`
	OperationName string                 `json:"operationName"`
}

func ParseGraphQLRequest(c *fiber.Ctx) (*GraphQLRequest, error) {
	var req GraphQLRequest
	if err := c.BodyParser(&req); err != nil {
		return nil, err
	}
	// check if the query is empty
	if req.Query == "" {
		return nil, fiber.NewError(fiber.StatusBadRequest, "missing query in request body")
	}

	return &req, nil
}

func IsMutationGraphQLRequest(req *GraphQLRequest) bool {
	// check if the query is a mutation with regex
	ok, _ := regexp.MatchString(`(?i)^\s*mutation\W`, req.Query)
	return ok
}

func IsSubscriptionGraphQLRequest(req *GraphQLRequest) bool {
	// check if the query is a subscription with regex
	ok, _ := regexp.MatchString(`(?i)^\s*subscription\W`, req.Query)
	return ok
}
