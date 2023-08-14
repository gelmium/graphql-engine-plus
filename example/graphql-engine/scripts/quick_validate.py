from aiohttp import web

# do not import here, must import in main() function

async def main(request: web.Request, body, transport):
    import copy
    from datetime import datetime
    import uuid
    import logging
    logger = logging.getLogger("quick_validate.py")
    # required params from body
    payload = body['payload']
    
    # logger.info(f"body: {body}")
    # {'payload': {'data': {'input': [{'external_ref_list': ['text_external_ref'], 'first_name': 'test', 'last_name': 'cus'}]}, 'role': 'user', 'session_variables': {'x-hasura-role': 'user', 'x-hasura-user-id': '1'}, 'version': 1}, 'execfile': 'quick_validate.py', 'execurl': None, 'execasync': None}
    # loop in body.payload.data.input to check if first_name = last_name
    # if so, raise error
    for input in payload['data']['input']:
        if input['first_name'] == input['last_name']:
            raise ValueError(f"first_name {input['first_name']} is same as last_name {input['last_name']}")
        

    # require redis client to be initialized in the server.py
    r = request.app["redis_client"]
    # logger.info(f"request headers: {request.headers}")
    x_request_id = request.headers.get('x-request-id')
    # check if x-request-id is exist in redis cache
    if x_request_id:
        # check if x-request-id is exist in redis cache
        if await r.get(x_request_id):
            raise ValueError(f"x-request-id {x_request_id} is exist in redis cache")
        # set x-request-id to redis cache
        await r.set(x_request_id, '1', 60*60*24)

    body['payload'] = payload