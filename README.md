# multisuper

Seis binarios, uno por cadena de supermercados, que convierten el catálogo de cada tienda en
**un mismo objeto JSON**.

El producto es una **cesta virtual**: el usuario elige los artículos que quiere y el sistema le
dice **cuánto le costaría esa cesta en cada supermercado y qué le faltaría en cada uno**, para
que decida dónde comprar. La cesta de la tienda —la de verdad, la que se paga— es solo el
**paso de entrega** de la cadena elegida, no el objetivo.

```
multisuper find   leche          → qué hay y a qué precio en las seis cadenas
multisuper basket -f cesta.json  → cuánto costaría TU cesta en cada una
<cadena> cart                     → llenar la cesta real de la cadena elegida
```

```
dia        → dia.es                          API de la app Android (endpoint /reduced)
mas        → supermercadosmas.com           Magento 2 GraphQL
aldi       → aldi.es                        Algolia (search-only), un índice por región
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

## Cesta virtual

El comando que responde a «¿dónde compro?»:

```bash
multisuper basket -f cesta.json --json
```

```json
{
  "items": [
    {"query":"leche entera","quantity":1,
     "resolved":{"dia":{"id":"312953","name":"Leche entera Gremlins Dia Láctea 3 x 200 ml","unitCents":92,"lineCents":92}},
     "missing":["lidl"]}
  ],
  "chains": [
    {"store":"dia","totalCents":417,"coverage":"3/3","elapsedMs":1177,"missing":[]},
    {"store":"lidl","totalCents":1308,"coverage":"2/3","elapsedMs":471,"missing":["leche entera"]},
    {"store":"carrefour","totalCents":0,"elapsedMs":20010,"missing":[],
     "error":{"code":"timeout","retryable":true,"message":"..."}}
  ],
  "ranking": [{"store":"dia","totalCents":417}]
}
```

Entrada: `{"items":[{"query":"leche entera","quantity":2}]}`, o `-f -` por stdin.
Salida: por cadena, **total en céntimos enteros** (nunca flotantes), cobertura (`3/3`), los
artículos que **faltan** y, cuando la consulta falló, su código de error. El campo `ranking`
contiene **solo las cadenas que cubren la cesta completa**, de más barata a más cara: si
ninguna la cubre, el ranking va vacío y el comando sale con `1`. Eso es una respuesta, no un
fallo.

La primera pasada real, con leche entera + pan de molde + huevos: **DIA 4,17 €**, MAS 4,38 €,
ALDI 4,88 €; LIDL no tenía leche entera; ALCAMPO estaba en ventana de WAF y CARREFOUR sin
cuota.

**El emparejamiento es una propuesta, no una verdad.** Con nombres de una sola palabra el
algoritmo confunde categorías: en esa misma pasada «huevos» se resolvió como *huevos de
chocolate* en ALDI y como un *cuecehuevos* en LIDL, y con huevos de verdad ALDI habría
ganado. Por eso el comando ofrece candidatos puntuados por artículo y la cesta acepta la
elección explícita del usuario:

```json
{"items":[{"store":"dia","id":"305802","name":"Huevos extragrandes L/XL ... Dia 6 unidades","quantity":2}]}
```

Una entrada fijada se resuelve **confirmando el id exacto**; si no aparece, esa cadena
responde `stale` (el producto existe para el usuario pero hoy no se puede confirmar) y queda
fuera del ranking. Nunca se sustituye por otro producto: un total equivocado es peor que un
total ausente.

## Cestas de tienda (paso de entrega)

Solo `dia` llena una cesta hoy, y de punta a punta:

```bash
dia cart add 504P6 1      # crea la sesión anónima si no existe
dia cart get --json       # líneas, precio unitario, total
dia cart update 504P6 2   # fija la cantidad
dia cart remove 504P6
dia cart clear
```

La identidad es la cookie `session_id` que DIA emite en la primera llamada, guardada en
`~/.config/multisuper/dia-cart.json` con permisos `0600`. Nunca hay login, ni checkout, ni
pedido: la cesta se llena y se abandona.

`mas` tiene el mismo juego de verbos (`mas cart add|get|remove|clear`), pero **solo lee y
vacía**: la tienda rechaza cualquier `add` (ver límites). Ninguna otra cadena tiene cesta.

**Los códigos de salida son los mismos en los seis binarios:**

| Código | Significado |
|---|---|
| `0` | éxito |
| `1` | error al obtener o al parsear la respuesta de la tienda |
| `2` | error de uso: falta el término, `--limit` fuera de 1..100, bandera desconocida |

`-h`/`--help` en cualquier posición imprime la ayuda en stdout y sale con `0`.

El término es **la frase completa**: `dia search leche entera` busca «leche entera». Un
`--limit` inválido se rechaza; no se recorta en silencio.

> `go run` no propaga el código de salida (imprime `exit status 2` y sale con 1). Para
> verificar códigos, construye el binario.

## Una llamada para las seis cadenas

La web no debe orquestar seis procesos ni leer prosa para saber qué falló:

```bash
multisuper find leche --limit 3 --json
multisuper find leche --only mas,dia --timeout 5s
```

Lanza las seis en paralelo con timeout por cadena y devuelve **siempre resultado parcial**:
una cadena caída no cancela las demás ni deja la respuesta vacía.

```json
{"query":"leche","count":15,"results":[
  {"store":"dia","count":3,"elapsedMs":420,"hits":[ ... ]},
  {"store":"carrefour","count":0,"elapsedMs":188,"hits":[],
   "error":{"code":"quota","retryable":true,"message":"..."}}
]}
```

Códigos de error estables, no texto: `blocked` (403/denegación de borde), `quota` (429 o
`202` de AWS WAF), `timeout`, `network`, `parse`, `unknown`. Solo `quota` y `timeout` son
`retryable`. Salida: `0` si al menos una cadena respondió, `1` si fallaron todas, `2` uso.

Sin caché y sin fusión de productos: un proceso de vida corta no puede cachear por la web, y
la fusión necesita un identificador común que hoy solo tiene Carrefour. Ambas cosas pertenecen
al servidor que envuelva estos binarios.

## Contrato JSON

Con `--json`, stdout lleva **exactamente un documento** y nada más; los errores y los avisos
van a stderr. Ejemplo real, reproducible con `dia search leche --limit 1 --json`:

```json
{
  "store": "dia",
  "query": "leche",
  "count": 1,
  "hits": [
    {
      "store": "dia",
      "id": "504P6",
      "name": "Leche semidesnatada Dia Láctea pack 6 x 1 L",
      "brand": "Dia Láctea",
      "price": 4.98,
      "pricePerUnit": 0.83,
      "unit": "L",
      "currency": "EUR",
      "available": true,
      "category": "Leche",
      "url": "https://www.dia.es/huevos-leche-y-mantequilla/leche/p/504P6",
      "image": "https://www.dia.es/product_images/504P6/504P6_ISO_0_ES.jpg"
    }
  ]
}
```

La regla es una sola: **lo que la tienda no publica, no aparece**.

| Campo | Regla |
|---|---|
| `price` / `currency` | número en euros, **u omitidos** si la tienda no publica precio. Un `0` publicado es un precio real; la ausencia es la única forma de decir «no publicado». `currency` nunca aparece sin `price`. |
| `pricePerUnit` / `unit` | solo desde datos publicados por la tienda. `unit` ∈ `kg` \| `L` \| `unit`. **Nunca `pricePerUnit` sin `unit`**: si la unidad no se entiende, se omiten los dos. |
| `available` | `true`/`false` cuando la tienda lo dice, **omitido cuando no lo dice**. Una clave ausente significa «no se sabe», no «no disponible». |
| `count` | número de hits devueltos, no el total del catálogo. |
| `id` | estable dentro de la cadena; será la clave del carrito futuro. |

Campos que cada cadena sí rellena: `mas` no publica tamaño de venta, así que **no da
`pricePerUnit`**; `carrefour` no publica stock, así que no da `available`; `mas` no devuelve
`brand`, `category` ni `image`.

**Identidad entre cadenas.** `ean` y `size` existen para que la web sepa que dos hits son el
mismo producto. La respuesta medida es dura: **solo Carrefour publica código de barras**
(Superfudis lo entrega, `8410297110979`). `mas` no puede exponerlo —su esquema GraphQL ni
siquiera tiene `custom_attributes`, donde Magento guarda los atributos propios—, y `dia`,
`aldi`, `lidl` y `alcampo` publican números de artículo internos, no EAN. Por tanto el
emparejamiento entre cadenas tendrá que ser por nombre + `size`, y `size` es texto publicado
sin normalizar (`"2200 ml"`, no `"2,2 l"`): normalizarlo es trabajo del agregador.

## Las seis cadenas

| Cadena | Endpoint | Auth | Rarezas que condicionan el código |
|---|---|---|---|
| `dia` | `GET www.dia.es/api/v1/search-back/search/reduced?q=&page=0` | ninguna | Es el endpoint de la **app Android**, no el del sitio web (`/search-back/search` devuelve 403 de Akamai). Exige las cabeceras de plataforma de la app (`X-Id-Application-From: 8`, `X-Mob-Platform-From: OneApp-Android`, `order_source: app`, `X-App-Supports-L1-As-L2`, `User-Agent: okhttp/4.12.0`…). Devuelve además `pagination`, `total_items`, `sort` y `suggestions`. La zona la fija el servidor: `cart.postal_code` y **no hay forma verificada de cambiarla**. |
| `mas` | `POST www.supermercadosmas.com/graphql/` | ninguna (carrito de invitado disponible) | La barra final importa: sin ella, 301 y Go pierde el cuerpo del POST. `final_price` **no existe** en este esquema (pedirlo rompe la consulta); se usa `price.regularPrice.amount`. El carrito expone `itemsV2`, no `items`. Pago con Mollie. |
| `aldi` | `POST <appid>-dsn.algolia.net/1/indexes/an_prd_es_es_<región>_products2/query` | credenciales search-only en el bundle | La región **es** el índice. Precios distintos por región (pen/can/bal). La ruta Next.js `/_next/data/<buildId>` está bloqueada con 308. |
| `lidl` | `GET www.lidl.es/q/api/search?q=&locale=es_ES&assortment=ES&version=2.1.0` | ninguna | Sin el parámetro `version` responde **401**; su valor no se valida. Hay productos cuyo único precio es de socio Lidl Plus: se dejan sin precio y se avisa por stderr. |
| `alcampo` | `GET compraonline.alcampo.es/api/webproductpagews/v6/product-pages/search` | `x-csrf-token` de la portada | Su GraphQL existe pero el tipo `Product` no lleva nombre ni precio. `/api/*` está tras AWS WAF: pasadas unas peticiones responde `202 challenge` durante minutos. |
| `carrefour` | `POST supercomparador-i9tw.onrender.com/api/v1/compare-basket` | ninguna | `carrefour.es` responde 403 de Cloudflare en todo el catálogo y en sus APIs (solo `robots.txt` y `favicon.ico` pasan la lista blanca). El precio es **reportado por Superfudis**, no publicado por Carrefour: así se declara en stderr. |

## Decisiones y por qué

- **Un binario por cadena** en lugar de un `--store`: cada tienda tiene su stack, su
  autenticación y sus rarezas. Lo único compartido es `internal/contract`.
- **Sin dependencias.** El módulo es stdlib puro. Hubo una excepción, `uTLS` con huella de
  Chrome, para pelear el 403 de DIA: se negoció h2 en 10 de 10 intentos y el borde respondió
  403 igual. La huella no era la puerta. Lo que la abrió fue **leer el cliente que sí
  funcionaba** (el APK de la app) y usar su endpoint y sus cabeceras. La dependencia se retiró.
- **Carrefour vía Superfudis (fuente declarada).** Cloudflare bloquea el catálogo entero y no
  hay endpoint de búsqueda ni dentro del navegador. Se lee la API pública del comparador, con
  su límite real (10 productos, 3 peticiones/minuto) y declarando la procedencia.
- **Nada se inventa, aunque cueste cobertura.** `mas` deducía el tamaño del nombre del
  producto y llegó a publicar `0,09 €/L` para una bolsa de basura de 30 litros; ese parser se
  eliminó. `dia` publica `price_per_unit` con unidades que no siempre entiende (`LAVADO`,
  `100 ML.`); cuando no las entiende, no publica precio unitario. Una cadena que no responde
  falla con error explícito en vez de devolver vacío.

## Verificación

```bash
go test ./... -count=1     # sin red: fixtures reales capturadas + httptest
go vet ./... && gofmt -l .
```

Cada paquete tiene su fixture capturado de la API real y tests que afirman la forma exacta
del JSON y la forma exacta de la petición (método, ruta, cabeceras). Los smokes reales se
hacen con el binario construido. Tres verificaciones independientes revisaron el proyecto:
la primera encontró tres defectos en `dia` (frase truncada, `--limit` recortado, códigos de
salida divergentes), la segunda cinco cadenas completas y siete hallazgos más (unidad sin
precio unitario, €/L inventados en `mas`, stock afirmado en `carrefour`, y cinco afirmaciones
falsas de este README).

## Límites conocidos

| Cadena | Límite |
|---|---|
| `dia` | Depende de las cabeceras de la app y de valores sintéticos de dispositivo (`X-Mob-Deviceid`, `X-Retail-Visitor-Id`); si DIA empieza a validarlos, habrá que revisarlos. |
| `alcampo` | Ventanas de `202 challenge` de AWS WAF tras varias peticiones seguidas. |
| `carrefour` | Depende de una API de terceros gratuita, con su cuota y su latencia (arranque en frío ~9 s). |
| `aldi`, `lidl` | Credenciales/parámetros que pueden rotar; fallan con error explícito si eso pasa. |
| `mas` | Sin €/unidad (no publica tamaño) y sin `brand`/`category`/`image` en la respuesta actual. |
| `aldi`, `lidl` | Publican el tamaño de venta como texto (`salesUnit`, `packaging.text`) y hoy no se expone en `size`: es la mejora más barata para ellas. |
| `carrefour` | Superfudis devolvió `500 basket_comparison_failed` en una llamada y 200 al reintentar; y agotó su cuota de 3/min en la primera prueba real del agregador. El `ean` depende de un tercero intermitente. |
| `mas` (cesta) | El carrito GraphQL lee, vacía y olvida el id, pero `addProductsToCart` es rechazado para todo SKU (`UNDEFINED: La cantidad solicitada no está disponible`): su canal de venta no expone inventario por GraphQL y la propia tienda añade con un **POST de formulario** a `/checkout/cart/add`. Esa vía exige `form_key` y cookies de sesión: pendiente. |
| `dia` (cesta) | El `total` es el subtotal de artículos, sin envío (DIA publica 14,95 € aparte, con 4,99 € de envío). La lectura de líneas vive bajo `api/v1/checkout-back/cart/reduced` porque es el único payload que las trae: es un GET, sin mutación ni pedido. DIA aplica su propio tope por línea (`20000` unidades → «solo 6 en stock» aunque la búsqueda diga 8564): el CLI repite su mensaje, nunca calcula el suyo. |
| cestas | Solo posibles donde hay venta online: `dia` **funciona**; `mas` solo lee; `lidl` y `alcampo` por verificar. **ALDI no vende online** y **Carrefour está bloqueado** (y Superfudis no gestiona cestas): esas dos nunca tendrán cesta, solo precio. |

## Origen

Contrato de datos inspirado en `jgalea/grocery-cli` (MIT) y en `ivorpad/mercadona-cli`, que
documenta el patrón de cliente HTTP con huella de navegador. Ninguno se importa; `internal/`
no es importable entre módulos. El análisis estático del APK de DIA está en
`odd/research/dia-android-api.md`.
