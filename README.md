## go-chat-app

Messages are stateless.

### \# server

```
go run main.go

> if self-signed certificate, open first:
https://localhost/
```

### \# client
```
go run client/client.go

> go to: http://localhost/
```

![img1](./img/img1.png)

\> connect **merry**
![img2](./img/img2.png)

\> connect **john**
![img3](./img/img3.png)

\> **john** sends message 'ping' to **merry**
![img4](./img/img4.png)

\> **merry** sends message 'pong' to **john**
![img5](./img/img5.png)
