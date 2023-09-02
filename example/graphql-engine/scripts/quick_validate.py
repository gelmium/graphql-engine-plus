from aiohttp import web

# do not import here, must import in main() function


async def main(request: web.Request, body):
    import logging

    logger = logging.getLogger("quick_validate.py")
    # required params from body
    payload = body["payload"]

    # loop in body.payload.data.input to check if first_name = last_name
    # if so, raise error
    for input in payload["data"]["input"]:
        if input["first_name"] == input.get("last_name"):
            raise ValueError(
                f"first_name {input['first_name']} is same as last_name {input['last_name']}"
            )
    # you can do more validation using database connection or redis cache
    body["payload"] = payload
