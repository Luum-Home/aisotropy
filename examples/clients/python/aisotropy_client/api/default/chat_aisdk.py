from http import HTTPStatus
from typing import Any, cast
from urllib.parse import quote

import httpx

from ...client import AuthenticatedClient, Client
from ...types import Response, UNSET
from ... import errors

from ...models.aisdk_request import AisdkRequest
from ...models.error_model import ErrorModel
from typing import cast



def _get_kwargs(
    tenant_id: str,
    user_id: str,
    *,
    body: AisdkRequest,

) -> dict[str, Any]:
    headers: dict[str, Any] = {}


    

    

    _kwargs: dict[str, Any] = {
        "method": "post",
        "url": "/v1/tenant/{tenant_id}/user/{user_id}/chat/aisdk".format(tenant_id=quote(str(tenant_id), safe=""),user_id=quote(str(user_id), safe=""),),
    }

    _kwargs["json"] = body.to_dict()

    headers["Content-Type"] = "application/json"

    _kwargs["headers"] = headers
    return _kwargs



def _parse_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> Any | ErrorModel:
    if response.status_code == 200:
        response_200 = cast(Any, None)
        return response_200

    response_default = ErrorModel.from_dict(response.json())



    return response_default



def _build_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> Response[Any | ErrorModel]:
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
    body: AisdkRequest,

) -> Response[Any | ErrorModel]:
    r""" Chat via the Vercel AI SDK UI Message Stream (Phase 2A adapter)

     Serves the SAME streaming pipeline as /chat/stream re-encoded as the Vercel AI SDK \"UI Message
    Stream\" (header x-vercel-ai-ui-message-stream: v1, SSE parts framed as data: ${JSON}, terminated by
    data: [DONE]). Consumed by @ai-sdk/react useChat and assistant-ui. The request body is the AI SDK
    {messages: UIMessage[]} envelope; the latest user message's text is decoded into the prompt.
    Tenant/user isolation, the quota gate, guardrails, memory hydration, and cost accounting are
    unchanged — this endpoint only re-encodes the output.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (AisdkRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[Any | ErrorModel]
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
    body: AisdkRequest,

) -> Any | ErrorModel | None:
    r""" Chat via the Vercel AI SDK UI Message Stream (Phase 2A adapter)

     Serves the SAME streaming pipeline as /chat/stream re-encoded as the Vercel AI SDK \"UI Message
    Stream\" (header x-vercel-ai-ui-message-stream: v1, SSE parts framed as data: ${JSON}, terminated by
    data: [DONE]). Consumed by @ai-sdk/react useChat and assistant-ui. The request body is the AI SDK
    {messages: UIMessage[]} envelope; the latest user message's text is decoded into the prompt.
    Tenant/user isolation, the quota gate, guardrails, memory hydration, and cost accounting are
    unchanged — this endpoint only re-encodes the output.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (AisdkRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Any | ErrorModel
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
    body: AisdkRequest,

) -> Response[Any | ErrorModel]:
    r""" Chat via the Vercel AI SDK UI Message Stream (Phase 2A adapter)

     Serves the SAME streaming pipeline as /chat/stream re-encoded as the Vercel AI SDK \"UI Message
    Stream\" (header x-vercel-ai-ui-message-stream: v1, SSE parts framed as data: ${JSON}, terminated by
    data: [DONE]). Consumed by @ai-sdk/react useChat and assistant-ui. The request body is the AI SDK
    {messages: UIMessage[]} envelope; the latest user message's text is decoded into the prompt.
    Tenant/user isolation, the quota gate, guardrails, memory hydration, and cost accounting are
    unchanged — this endpoint only re-encodes the output.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (AisdkRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[Any | ErrorModel]
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
    body: AisdkRequest,

) -> Any | ErrorModel | None:
    r""" Chat via the Vercel AI SDK UI Message Stream (Phase 2A adapter)

     Serves the SAME streaming pipeline as /chat/stream re-encoded as the Vercel AI SDK \"UI Message
    Stream\" (header x-vercel-ai-ui-message-stream: v1, SSE parts framed as data: ${JSON}, terminated by
    data: [DONE]). Consumed by @ai-sdk/react useChat and assistant-ui. The request body is the AI SDK
    {messages: UIMessage[]} envelope; the latest user message's text is decoded into the prompt.
    Tenant/user isolation, the quota gate, guardrails, memory hydration, and cost accounting are
    unchanged — this endpoint only re-encodes the output.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (AisdkRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Any | ErrorModel
     """


    return (await asyncio_detailed(
        tenant_id=tenant_id,
user_id=user_id,
client=client,
body=body,

    )).parsed
