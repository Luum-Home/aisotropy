from http import HTTPStatus
from typing import Any, cast
from urllib.parse import quote

import httpx

from ...client import AuthenticatedClient, Client
from ...types import Response, UNSET
from ... import errors

from ...models.chat_request import ChatRequest
from ...models.chat_stream_event_done import ChatStreamEventDone
from ...models.chat_stream_event_error import ChatStreamEventError
from ...models.chat_stream_event_token import ChatStreamEventToken
from ...models.error_model import ErrorModel
from typing import cast



def _get_kwargs(
    tenant_id: str,
    user_id: str,
    *,
    body: ChatRequest,

) -> dict[str, Any]:
    headers: dict[str, Any] = {}


    

    

    _kwargs: dict[str, Any] = {
        "method": "post",
        "url": "/v1/tenant/{tenant_id}/user/{user_id}/chat/stream".format(tenant_id=quote(str(tenant_id), safe=""),user_id=quote(str(user_id), safe=""),),
    }

    _kwargs["json"] = body.to_dict()

    headers["Content-Type"] = "application/json"

    _kwargs["headers"] = headers
    return _kwargs



def _parse_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> ErrorModel | list[ChatStreamEventDone | ChatStreamEventError | ChatStreamEventToken]:
    if response.status_code == 200:
        response_200 = []
        _response_200 = response.text
        for response_200_item_data in (_response_200):
            def _parse_response_200_item(data: object) -> ChatStreamEventDone | ChatStreamEventError | ChatStreamEventToken:
                try:
                    if not isinstance(data, dict):
                        raise TypeError()
                    response_200_item_event_done = ChatStreamEventDone.from_dict(data)



                    return response_200_item_event_done
                except (TypeError, ValueError, AttributeError, KeyError):
                    pass
                try:
                    if not isinstance(data, dict):
                        raise TypeError()
                    response_200_item_event_error = ChatStreamEventError.from_dict(data)



                    return response_200_item_event_error
                except (TypeError, ValueError, AttributeError, KeyError):
                    pass
                if not isinstance(data, dict):
                    raise TypeError()
                response_200_item_event_token = ChatStreamEventToken.from_dict(data)



                return response_200_item_event_token

            response_200_item = _parse_response_200_item(response_200_item_data)

            response_200.append(response_200_item)

        return response_200

    response_default = ErrorModel.from_dict(response.json())



    return response_default



def _build_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> Response[ErrorModel | list[ChatStreamEventDone | ChatStreamEventError | ChatStreamEventToken]]:
    return Response(
        status_code=HTTPStatus(response.status_code),
        content=response.content,
        headers=response.headers,
        parsed=_parse_response(client=client, response=response),
    )


def sync_detailed(
    tenant_id: str,
    user_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: ChatRequest,

) -> Response[ErrorModel | list[ChatStreamEventDone | ChatStreamEventError | ChatStreamEventToken]]:
    """ Chat with the agent via SSE streaming (Phase 1+)

     Streams the LLM response token by token, hydrating the system prompt from the session's working
    memory (summary + recent messages) and persisting both the user and assistant turns once the stream
    completes successfully. Uses ANTHROPIC_API_KEY if set; otherwise falls back to the canned stub
    (internal/llm) so the SSE path can be verified without credentials. Honors ctx cancellation: if the
    client cuts the connection, the stream stops immediately and no partial assistant turn is persisted.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (ChatRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[ErrorModel | list[ChatStreamEventDone | ChatStreamEventError | ChatStreamEventToken]]
     """


    kwargs = _get_kwargs(
        tenant_id=tenant_id,
user_id=user_id,
body=body,

    )

    response = client.get_httpx_client().request(
        **kwargs,
    )

    return _build_response(client=client, response=response)

def sync(
    tenant_id: str,
    user_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: ChatRequest,

) -> ErrorModel | list[ChatStreamEventDone | ChatStreamEventError | ChatStreamEventToken] | None:
    """ Chat with the agent via SSE streaming (Phase 1+)

     Streams the LLM response token by token, hydrating the system prompt from the session's working
    memory (summary + recent messages) and persisting both the user and assistant turns once the stream
    completes successfully. Uses ANTHROPIC_API_KEY if set; otherwise falls back to the canned stub
    (internal/llm) so the SSE path can be verified without credentials. Honors ctx cancellation: if the
    client cuts the connection, the stream stops immediately and no partial assistant turn is persisted.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (ChatRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        ErrorModel | list[ChatStreamEventDone | ChatStreamEventError | ChatStreamEventToken]
     """


    return sync_detailed(
        tenant_id=tenant_id,
user_id=user_id,
client=client,
body=body,

    ).parsed

async def asyncio_detailed(
    tenant_id: str,
    user_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: ChatRequest,

) -> Response[ErrorModel | list[ChatStreamEventDone | ChatStreamEventError | ChatStreamEventToken]]:
    """ Chat with the agent via SSE streaming (Phase 1+)

     Streams the LLM response token by token, hydrating the system prompt from the session's working
    memory (summary + recent messages) and persisting both the user and assistant turns once the stream
    completes successfully. Uses ANTHROPIC_API_KEY if set; otherwise falls back to the canned stub
    (internal/llm) so the SSE path can be verified without credentials. Honors ctx cancellation: if the
    client cuts the connection, the stream stops immediately and no partial assistant turn is persisted.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (ChatRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[ErrorModel | list[ChatStreamEventDone | ChatStreamEventError | ChatStreamEventToken]]
     """


    kwargs = _get_kwargs(
        tenant_id=tenant_id,
user_id=user_id,
body=body,

    )

    response = await client.get_async_httpx_client().request(
        **kwargs
    )

    return _build_response(client=client, response=response)

async def asyncio(
    tenant_id: str,
    user_id: str,
    *,
    client: AuthenticatedClient | Client,
    body: ChatRequest,

) -> ErrorModel | list[ChatStreamEventDone | ChatStreamEventError | ChatStreamEventToken] | None:
    """ Chat with the agent via SSE streaming (Phase 1+)

     Streams the LLM response token by token, hydrating the system prompt from the session's working
    memory (summary + recent messages) and persisting both the user and assistant turns once the stream
    completes successfully. Uses ANTHROPIC_API_KEY if set; otherwise falls back to the canned stub
    (internal/llm) so the SSE path can be verified without credentials. Honors ctx cancellation: if the
    client cuts the connection, the stream stops immediately and no partial assistant turn is persisted.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (ChatRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        ErrorModel | list[ChatStreamEventDone | ChatStreamEventError | ChatStreamEventToken]
     """


    return (await asyncio_detailed(
        tenant_id=tenant_id,
user_id=user_id,
client=client,
body=body,

    )).parsed
