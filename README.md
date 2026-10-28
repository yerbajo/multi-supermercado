# multisuper

Seis binarios, uno por cadena de supermercados, que convierten el catálogo de cada tienda en
**un mismo objeto JSON**. Nacen para alimentar un agregador web que busca un producto en todas
las cadenas y compara €/unidad; el carrito de cada tienda es una fase posterior.

```
dia        → dia.es                          REST propio (search-back)
mas        → supermercadosmas.com           Magento 2 GraphQL
aldi       → aldi.es                        Algolia (search-only)
lidl       → lidl.es                        API propia con parámetro `version`
alcampo    → compraonline.alcampo.es        REST tras AWS WAF
carrefour  → vía Superfudis (tercero)       API pública del comparador
```

## Uso

```bash
go build -o bin/ ./cmd/...        # seis binarios
bin/mas search leche --limit 3 --json
bin/dia search leche entera        # tabla legible por defecto
```

Comando único en todas las cadenas: `search <término> [--limit N] [--json]`.
`aldi` añade `--region pen|can|bal` y `mas` añade `--page N`.

**Los códigos de salida son los mismos en los seis binarios:**

| Código | Significado |
|---|---|
| `0` | éxito |
| `1` | error al obtener o al parsear la respuesta de la tienda |
| `2` | error de uso: falta el término, `--limit` fuera de 1..100, bandera desconocida |

El término es **la frase completa**: `dia search leche entera` busca «leche entera». Un
`--limit` inválido se rechaza; no se recorta en silencio.

> `go run` no propaga el código de salida (imprime `exit status 2` y sale con 1). Para
> verificar códigos, construye el binario.

## Contrato JSON

Con `--json`, stdout lleva **exactamente un documento** y nada más; los errores y los avisos
van a stderr.

```json
{
  "store": "mas",
  "query": "leche",
  "count": 3,
  "hits": [
    {
      "store": "mas",
      "id": "114042",
      "name": "Leche condensada LA LECHERA 370 gr",
      "brand": "LA LECHERA",
      "price": 2.89,
      "pricePerUnit": 7.81,
      "unit": "kg",
      "currency": "EUR",
      "available": true,
      "category": "Lacteos sustitutivos",
      "url": "https://www.supermercadosmas.com/lacteos-sustitutivos/leche-condensada-la-lechera-370-gr/",
      "image": "https://…"
    }
  ]
}
```

- `price`: número en euros, **u omitido** cuando la tienda no publica precio. Un `0` publicado
  es un precio real; la ausencia es la única forma de decir «no publicado».
- `pricePerUnit` + `unit` (`kg` | `L` | `unit`): solo desde datos publicados por la tienda
  (precio unitario propio, o tamaño declarado). Nunca se deduce del nombre si la tienda no lo
  publica, salvo en MAS y en Lidl, donde el tamaño forma parte del dato de venta.
- `count` = número de hits devueltos, no el total del catálogo.
- `id` es estable dentro de la cadena y sirve de clave para el carrito futuro.

## Las seis cadenas

| Cadena | Endpoint real | Auth | Rarezas que condicionan el código |
|---|---|---|---|
| `dia` | `GET www.dia.es/api/v1/search-back/search?q=` | ninguna | Trae `price_per_unit` y `measure_unit` (KILO/LITRO) ya calculados. La zona la fija el servidor: la respuesta lleva `cart.postal_code` y **no hay forma verificada de cambiarla**. |
| `mas` | `POST www.supermercadosmas.com/graphql` | ninguna (carrito de invitado disponible) | `price_range` devuelve 500 y `finalPrice` no existe: **solo** `price.regularPrice.amount`. El carrito usa `itemsV2`, no `items`. Pago con Mollie. |
| `aldi` | `POST <appid>-dsn.algolia.net/1/indexes/an_prd_es_es_<región>_products2/query` | credenciales search-only en el bundle | La región **es** el índice. Precios distintos por región (pen/can/bal). La ruta Next.js `/_next/data/<buildId>` está bloqueada con 308. |
| `lidl` | `GET www.lidl.es/q/api/search?q=&locale=es_ES&assortment=ES&version=2.1.0` | ninguna | Sin el parámetro `version` responde **401**; su valor no se valida. Hay productos cuyo precio solo existe para socios Lidl Plus: se dejan sin precio. |
| `alcampo` | `GET compraonline.alcampo.es/api/webproductpagews/v6/product-pages/search` | `x-csrf-token` de la portada | El GraphQL existe pero su tipo `Product` no lleva nombre ni precio. `/api/*` está tras AWS WAF: pasadas unas peticiones responde `202 challenge` durante minutos. |
| `carrefour` | `POST supercomparador-i9tw.onrender.com/api/v1/compare-basket` | ninguna | `carrefour.es` está bloqueado por Cloudflare en **todas** sus rutas. El precio es **reportado por Superfudis**, no publicado por Carrefour; así se declara en stderr. |

## Decisiones y por qué

- **Un binario por cadena** en lugar de un `--store`: cada tienda tiene su stack, su
  autenticación y sus rarezas. Lo único compartido es `internal/contract` (el sobre JSON).
- **Sin dependencias salvo uTLS.** El proyecto era stdlib puro; uTLS entró para pelear el 403
  de DIA y **no lo desbloqueó** (ver abajo). Queda por su valor de diagnóstico y por el
  fallback probado.
- **Carrefour vía Superfudis (fuente declarada).** Cloudflare bloquea el catálogo entero y no
  hay endpoint de búsqueda ni dentro del navegador. Se lee la API pública del comparador, con
  su límite real (10 productos, 3 peticiones/minuto) y declarando la procedencia.
- **DIA: la huella TLS no era la puerta.** Con `uTLS` presentando el ClientHello de Chrome y
  h2 negociado, `dia.es` siguió respondiendo **403 en 10 de 10 intentos**. El siguiente
  diagnóstico es comparar la IP de salida, no el fingerprint.
- **Nada se inventa.** Sin precio publicado → campo ausente. Sin unidad publicada → sin
  €/unidad. Una cadena que no responde falla con error explícito en vez de devolver vacío.

## Verificación

```bash
go test ./... -count=1     # sin red: fixtures reales capturadas + httptest
go vet ./... && gofmt -l .
```

Cada paquete tiene su fixture capturado de la API real y tests que afirman la forma exacta
del JSON y la forma exacta de la petición (método, ruta, cabeceras). Los smokes reales se
hacen con el binario construido.

## Límites conocidos

| Cadena | Límite |
|---|---|
| `dia` | 403 de Akamai desde esta red, de forma intermitente e independiente de las cabeceras. |
| `alcampo` | Ventanas de `202 challenge` de AWS WAF tras varias peticiones seguidas. |
| `carrefour` | Depende de una API de terceros gratuita, con su cuota y su latencia (arranque en frío ~9 s). |
| `aldi`, `lidl` | Credenciales/parámetros que pueden rotar; fallan con error explícito si eso pasa. |
| `mas` | `count` no usa el `total_count` de Magento: no hay paginación expuesta al consumidor. |

## Origen

Contrato de datos inspirado en `jgalea/grocery-cli` (MIT) y en `ivorpad/mercadona-cli`, que
documenta el patrón de cliente HTTP con huella de navegador. Ninguno se importa; `internal/`
no es importable entre módulos.
