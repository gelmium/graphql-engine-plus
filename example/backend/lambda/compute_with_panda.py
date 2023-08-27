import json
import pandas
import time


def ping(request, body):
    body["payload"] = {"message": "pong"}
    return 200


def main(request, body):
    # read dataframes from body['payload'] with first row as header
    df = pandas.DataFrame.from_dict(body["payload"], orient="columns")
    # confirm the headers contain the required fields: ticker, price, timestamp
    if not {"ticker", "price"}.issubset(df.columns):
        raise ValueError(f"required columns are not exist: {df.columns}")
    # calculate the mean price and begin price for each ticker
    avg_df = df.groupby(["ticker"]).agg({"price": ["mean", lambda x: x.iloc[0]]})
    # reset the index to get ticker as column
    avg_df = avg_df.reset_index()
    # rename the column name to price
    avg_df.columns = ["ticker", "mean", "begin"]
    # use avg_df to set the avg_price for each ticker
    df["avg_price"] = df["ticker"].map(avg_df.set_index("ticker")["mean"])
    df["begin_price"] = df["ticker"].map(avg_df.set_index("ticker")["begin"])
    # use transform to get the average price for each ticker
    df["pct_change_from_avg"] = (df["price"] - df["avg_price"]) / df["avg_price"]
    # calculate the percentage change compare to first price of each ticker for each timestamp
    # use iloc[0] to get the first row of each ticker
    df["pct_change_from_begin"] = (df["price"] - df["begin_price"]) / df["begin_price"]
    # convert the dataframe to json without avg_price column
    # round the pct_change_from_avg, pct_change_from_begin to 2 decimal places and multiply by 100 to get percentage
    df = df.round({"pct_change_from_avg": 2, "pct_change_from_begin": 2})
    df["pct_change_from_avg"] = df["pct_change_from_avg"] * 100
    df["pct_change_from_begin"] = df["pct_change_from_begin"] * 100
    # round the avg_price to 1 decimal places
    avg_df = avg_df.round({"price": 1})
    ticker_json_data = df.drop(columns=["price", "avg_price", "begin_price"]).to_dict(
        orient="records"
    )
    # convert the avg_df to json with ticker as key
    avg_price_json_data = avg_df.set_index("ticker").to_dict(orient="index")

    body["payload"] = {
        "ticker_performance": ticker_json_data,
        "ticker_avg_price": avg_price_json_data,
    }
    return 200


# AWS Lambda Function Handler
def lambda_handler(event, context):
    start_time = time.time()
    # read the event body
    # print(f"event: {event}")
    # print(f"context: {context}")
    if event["headers"].get("content-type") == "application/json":
        body = json.loads(event["body"])
    else:
        body = event["body"]
    # route the request to the corresponding function
    if event.get("rawPath") == "/ping":
        status = ping(event, body)
    else:
        status = main(event, body)
    return {
        "statusCode": status,
        "headers": {
            "Content-Type": "application/json",
            "X-Execution-Time": f"{time.time() - start_time}",
        },
        "body": json.dumps(body["payload"]),
    }
