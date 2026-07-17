from http import HTTPStatus
from typing import Any, cast
from urllib.parse import quote

import httpx

from ...client import AuthenticatedClient, Client
from ...types import Response, UNSET
from ... import errors

from ...models.error_model import ErrorModel
from ...models.knowledge_delete_output_body import KnowledgeDeleteOutputBody
from typing import cast



def _get_kwargs(
    tenant_id: str,
    user_id: str,
    doc_id: str,

) -> dict[str, Any]:
    

    

    

    _kwargs: dict[str, Any] = {
        "method": "delete",
        "url": "/v1/tenant/{tenant_id}/user/{user_id}/knowledge/{doc_id}".format(tenant_id=quote(str(tenant_id), safe=""),user_id=quote(str(user_id), safe=""),doc_id=quote(str(doc_id), safe=""),),
    }


    return _kwargs



def _parse_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> ErrorModel | KnowledgeDeleteOutputBody:
    if response.status_code == 200:
        response_200 = KnowledgeDeleteOutputBody.from_dict(response.json())



        return response_200

    response_default = ErrorModel.from_dict(response.json())



    return response_default



def _build_response(*, client: AuthenticatedClient | Client, response: httpx.Response) -> Response[ErrorModel | KnowledgeDeleteOutputBody]:
    return Response(
        status_code=HTTPStatus(response.status_code),
        content=response.content,
        headers=response.headers,
        parsed=_parse_response(client=client, response=response),
    )


def sync_detailed(
    tenant_id: str,
    user_id: str,
    doc_id: str,
    *,
    client: AuthenticatedClient | Client,

) -> Response[ErrorModel | KnowledgeDeleteOutputBody]:
    """ Delete a document's chunks from the semantic tier (KindDocument, ADR-0018 open q#3)

     Validates the contract, isolates by tenant/user via the scoped store call, and removes every
    KindDocument chunk previously ingested under doc_id — idempotent: deleting an unknown or already-
    deleted doc_id returns deleted:0, never an error.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        doc_id (str): Document id previously used to ingest this document

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[ErrorModel | KnowledgeDeleteOutputBody]
     """


    kwargs = _get_kwargs(
        tenant_id=tenant_id,
user_id=user_id,
doc_id=doc_id,

    )

    response = client.get_httpx_client().request(
        **kwargs,
    )

    return _build_response(client=client, response=response)

def sync(
    tenant_id: str,
    user_id: str,
    doc_id: str,
    *,
    client: AuthenticatedClient | Client,

) -> ErrorModel | KnowledgeDeleteOutputBody | None:
    """ Delete a document's chunks from the semantic tier (KindDocument, ADR-0018 open q#3)

     Validates the contract, isolates by tenant/user via the scoped store call, and removes every
    KindDocument chunk previously ingested under doc_id — idempotent: deleting an unknown or already-
    deleted doc_id returns deleted:0, never an error.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        doc_id (str): Document id previously used to ingest this document

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        ErrorModel | KnowledgeDeleteOutputBody
     """


    return sync_detailed(
        tenant_id=tenant_id,
user_id=user_id,
doc_id=doc_id,
client=client,

    ).parsed

async def asyncio_detailed(
    tenant_id: str,
    user_id: str,
    doc_id: str,
    *,
    client: AuthenticatedClient | Client,

) -> Response[ErrorModel | KnowledgeDeleteOutputBody]:
    """ Delete a document's chunks from the semantic tier (KindDocument, ADR-0018 open q#3)

     Validates the contract, isolates by tenant/user via the scoped store call, and removes every
    KindDocument chunk previously ingested under doc_id — idempotent: deleting an unknown or already-
    deleted doc_id returns deleted:0, never an error.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        doc_id (str): Document id previously used to ingest this document

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[ErrorModel | KnowledgeDeleteOutputBody]
     """


    kwargs = _get_kwargs(
        tenant_id=tenant_id,
user_id=user_id,
doc_id=doc_id,

    )

    response = await client.get_async_httpx_client().request(
        **kwargs
    )

    return _build_response(client=client, response=response)

async def asyncio(
    tenant_id: str,
    user_id: str,
    doc_id: str,
    *,
    client: AuthenticatedClient | Client,

) -> ErrorModel | KnowledgeDeleteOutputBody | None:
    """ Delete a document's chunks from the semantic tier (KindDocument, ADR-0018 open q#3)

     Validates the contract, isolates by tenant/user via the scoped store call, and removes every
    KindDocument chunk previously ingested under doc_id — idempotent: deleting an unknown or already-
    deleted doc_id returns deleted:0, never an error.

    Args:
        tenant_id (str): Unique customer/tenant ID
        user_id (str): Unique user ID within the tenant
        doc_id (str): Document id previously used to ingest this document

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        ErrorModel | KnowledgeDeleteOutputBody
     """


    return (await asyncio_detailed(
        tenant_id=tenant_id,
user_id=user_id,
doc_id=doc_id,
client=client,

    )).parsed
