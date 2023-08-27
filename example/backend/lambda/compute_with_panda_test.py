import json
import unittest
from compute_with_panda import lambda_handler


class TestComputeWithPanda(unittest.TestCase):
    def test_lambda_handler_ping(self):
        # create a sample event
        event = {
            "headers": {},
            "rawPath": "/ping",
            "body": {"payload": {"message": "ping"}},
        }
        # call the lambda handler
        response = lambda_handler(event, None)
        # check the response status code
        self.assertEqual(response["statusCode"], 200)
        # check the response body
        self.assertDictEqual(json.loads(response["body"]), {"message": "pong"})

    def test_lambda_handler_main(self):
        # create a sample event
        event = {
            "headers": {},
            "body": {
                "payload": [
                    {
                        "ticker": "AAPL",
                        "price": 100,
                        "timestamp": "2022-01-01T00:00:00Z",
                    },
                    {
                        "ticker": "AAPL",
                        "price": 110,
                        "timestamp": "2022-01-02T00:00:00Z",
                    },
                    {
                        "ticker": "AAPL",
                        "price": 120,
                        "timestamp": "2022-01-03T00:00:00Z",
                    },
                    {
                        "ticker": "GOOG",
                        "price": 200,
                        "timestamp": "2022-01-01T00:00:00Z",
                    },
                    {
                        "ticker": "GOOG",
                        "price": 210,
                        "timestamp": "2022-01-02T00:00:00Z",
                    },
                    {
                        "ticker": "GOOG",
                        "price": 220,
                        "timestamp": "2022-01-03T00:00:00Z",
                    },
                ]
            },
        }
        print(json.dumps(event))
        # call the lambda handler
        response = lambda_handler(event, None)
        # check the response status code
        self.assertEqual(response["statusCode"], 200)
        # check the response body
        ticker_avg_price = {
            "AAPL": {"mean": 110.0, "begin": 100.0},
            "GOOG": {"mean": 210.0, "begin": 200.0},
        }
        result = json.loads(response["body"])

        self.assertDictEqual(result["ticker_avg_price"], ticker_avg_price)
        # expected ticker performance value with rouding to 2 decimal places
        ticker_performance = [
            {
                "ticker": "AAPL",
                "timestamp": "2022-01-01T00:00:00Z",
                "pct_change_from_avg": -9.0,
                "pct_change_from_begin": 0.0,
            },
            {
                "ticker": "AAPL",
                "timestamp": "2022-01-02T00:00:00Z",
                "pct_change_from_avg": 0.0,
                "pct_change_from_begin": 10.0,
            },
            {
                "ticker": "AAPL",
                "timestamp": "2022-01-03T00:00:00Z",
                "pct_change_from_avg": 9.0,
                "pct_change_from_begin": 20.0,
            },
            {
                "ticker": "GOOG",
                "timestamp": "2022-01-01T00:00:00Z",
                "pct_change_from_avg": -5.0,
                "pct_change_from_begin": 0.0,
            },
            {
                "ticker": "GOOG",
                "timestamp": "2022-01-02T00:00:00Z",
                "pct_change_from_avg": 0.0,
                "pct_change_from_begin": 5.0,
            },
            {
                "ticker": "GOOG",
                "timestamp": "2022-01-03T00:00:00Z",
                "pct_change_from_avg": 5.0,
                "pct_change_from_begin": 10.0,
            },
        ]
        # result = json.loads(result["ticker_performance"])
        for i in range(len(ticker_performance)):
            self.assertDictEqual(result["ticker_performance"][i], ticker_performance[i])
