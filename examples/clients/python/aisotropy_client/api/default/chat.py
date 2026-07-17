from http import HTTPStatus
from typing import Any, cast
from urllib.parse import quote

import httpx

from ...client import AuthenticatedClient, Client
from ...types import Response, UNSET
from ... import errors

from ...models.chat_output_body import ChatOutputBody
from ...models.chat_request import ChatRequest
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
        "url": "/v1/tenant/{tenant_id}/user/{user_id}/chat".format(tenant_id=quote(str(tenant_id), safe=""),user_id=quote(str(user_id), safe=""),),
    }

    _kwargs["json"] = body.to_dict()

    headers["Content-Type"] = "application/json"

    _kwargs["headers"] = headers
    return _kwargs



def _parse_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> ChatOutputBody | ErrorModel:
    if response.status_code == 200:
        response_200 = ChatOutputBody.from_dict(response.json())



        return response_200

    response_default = ErrorModel.from_dict(response.json())



    return response_default



def _build_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> Response[ChatOutputBody | ErrorModel]:
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

) -> Response[ChatOutputBody | ErrorModel]:
    """ Chat with the agent (structured output via the reflection loop)

     Validates the contract, isolates by tenant/user via ctx, hydrates the session's working memory into
    the system prompt, and enforces the reply against a JSON schema via the reflection loop
    (ReflectAuto: Gollem by default, with a homegrown fallback backend per ADR-0008). A persistent
    schema violation fails closed with an HTTP error — never a silent unstructured fallback (see
    .claude/rules/constitutional-gates.md).

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (ChatRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[ChatOutputBody | ErrorModel]
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

) -> ChatOutputBody | ErrorModel | None:
    """ Chat with the agent (structured output via the reflection loop)

     Validates the contract, isolates by tenant/user via ctx, hydrates the session's working memory into
    the system prompt, and enforces the reply against a JSON schema via the reflection loop
    (ReflectAuto: Gollem by default, with a homegrown fallback backend per ADR-0008). A persistent
    schema violation fails closed with an HTTP error — never a silent unstructured fallback (see
    .claude/rules/constitutional-gates.md).

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (ChatRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        ChatOutputBody | ErrorModel
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

) -> Response[ChatOutputBody | ErrorModel]:
    """ Chat with the agent (structured output via the reflection loop)

     Validates the contract, isolates by tenant/user via ctx, hydrates the session's working memory into
    the system prompt, and enforces the reply against a JSON schema via the reflection loop
    (ReflectAuto: Gollem by default, with a homegrown fallback backend per ADR-0008). A persistent
    schema violation fails closed with an HTTP error — never a silent unstructured fallback (see
    .claude/rules/constitutional-gates.md).

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (ChatRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[ChatOutputBody | ErrorModel]
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

) -> ChatOutputBody | ErrorModel | None:
    """ Chat with the agent (structured output via the reflection loop)

     Validates the contract, isolates by tenant/user via ctx, hydrates the session's working memory into
    the system prompt, and enforces the reply against a JSON schema via the reflection loop
    (ReflectAuto: Gollem by default, with a homegrown fallback backend per ADR-0008). A persistent
    schema violation fails closed with an HTTP error — never a silent unstructured fallback (see
    .claude/rules/constitutional-gates.md).

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        body (ChatRequest):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        ChatOutputBody | ErrorModel
     """


    return (await asyncio_detailed(
        tenant_id=tenant_id,
user_id=user_id,
client=client,
body=body,

    )).parsed
