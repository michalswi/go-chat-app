## go-chat-app

Messages are stateless.

Update `certs/` dir with **cert** and **key** .  
Update [index.html](./client/static/index.html) with **server's** IP instead of **localhost** .


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

\> connect **john** and send `ping` to **merry**
![img2](./img/img2.png)

\> connected **merry** and received message from **john**
![img3](./img/img3.png)
