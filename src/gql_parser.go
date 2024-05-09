package main

import (
	"regexp"
	"strconv"

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

var mutationRegex = regexp.MustCompile(`(?i)^\s*mutation\W`)
var subscriptionRegex = regexp.MustCompile(`(?i)^\s*subscription\W`)
var cachedQueryRegex = regexp.MustCompile(`(?i)^\s*query\W+\w*.*\s*@cached`)
var cachedParamsRegex = regexp.MustCompile(`@cached\((ttl|refresh)\s*:\s*(\d+|true)\)`)

func (req *GraphQLRequest) IsMutationGraphQLRequest() bool {
	// check if the query is a mutation with regex
	return mutationRegex.MatchString(req.Query)
}

func (req *GraphQLRequest) IsSubscriptionGraphQLRequest() bool {
	// check if the query is a subscription with regex
	return subscriptionRegex.MatchString(req.Query)
}

// return 0 if the query is not a cached query
// return -1 if the query is a cached query with indefinite ttl
// return ttl (seconds) if the query is a cached query with ttl.
func (req *GraphQLRequest) IsCachedQueryGraphQLRequest() int {
	// According to Hasura default max ttl is 3600 seconds
	maxTTL := int(hasuraGqlCacheMaxEntryTtl)
	// check if the query is a subscription with regex
	if cachedQueryRegex.MatchString(req.Query) {
		match := cachedParamsRegex.FindStringSubmatch(req.Query)
		// Group 0 is the whole match
		// Group 1 is the first group -> Key
		// Group 2 is the second group -> Value
		if len(match) >= 3 {
			if match[1] == "ttl" {
				// convert string match[1] to int
				if ttl, err := strconv.Atoi(match[2]); err == nil {
					if ttl > maxTTL {
						return maxTTL
					}
					return ttl
				}
			} else if match[1] == "refresh" && match[2] == "true" {
				return 0
			}
		}
		return maxTTL
	}
	return 0
}
