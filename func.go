package viot
	
import(
	"github.com/456vv/verror"
	"bytes"
	"strings"
	"bufio"
	"encoding/json"
	"strconv"
	"net"
	"net/url"
	"net/textproto"
	"io"
	"sync"
	"encoding/base64"
	"crypto/rand"
    "math"
    "math/big"	
	"golang.org/x/net/http/httpguts"
)


//在切片中查找
func strSliceContains(ss []string, t string) bool {
	for _, v := range ss {
		if v == t {
			return true
		}
	}
	return false
}

//判断方法
func validMethod(method string) bool {
  	return len(method) > 0 && strSliceContains(methods, method)
}

//判断协议
func validNPN(proto string) bool {
  	switch proto {
  	case "", "http/1.1", "http/1.0":
  		return false
  	}
  	return true
}


//解析IOT请求版本
//	vers string			版本字符串
//	major, minor int	大版号，小版号
//	ok bool				true版本正确解析，否则失败
func ParseIOTVersion(vers string) (major, minor int, ok bool) {
  	const Big = 1000000 // arbitrary upper bound
  	switch vers {
  	case "IOT/1.1":
  		return 1, 1, true
  	case "IOT/1.0":
  		return 1, 0, true
  	}
  	if !strings.HasPrefix(vers, "IOT/") {
  		return 0, 0, false
  	}
  	dot := strings.Index(vers, ".")
  	if dot < 0 {
  		return 0, 0, false
  	}
  	major, err := strconv.Atoi(vers[4:dot])
  	if err != nil || major < 0 || major > Big {
  		return 0, 0, false
  	}
  	minor, err = strconv.Atoi(vers[dot+1:])
  	if err != nil || minor < 0 || minor > Big {
  		return 0, 0, false
  	}
  	return major, minor, true
}

var textprotoReaderPool sync.Pool
//回收文本格式读取
func newTextprotoReader(br *bufio.Reader) *textproto.Reader {
	if v := textprotoReaderPool.Get(); v != nil {
		tr := v.(*textproto.Reader)
		tr.R = br
		return tr
	}
	return textproto.NewReader(br)
}

//提取文本格式读取
func putTextprotoReader(r *textproto.Reader) {
	r.R = nil
	textprotoReaderPool.Put(r)
}

//读取请求数据
//	b io.Reader		需解析的数据，重要提醒：不要包含多个json块，它只能解析一个json块，其它数据块会被丢弃。这会清空你的io.Reader。
//	req *Request	请求
//	err error		错误
func ReadRequest(b io.Reader) (req *Request, err error) {
	return readRequest(b)
}
func readRequest(b io.Reader) (req *Request, err error) {
	bufr := newBufioReader(b)
  	defer func(){
 		putBufioReader(bufr)
 		if err == io.EOF {
  			err = io.ErrUnexpectedEOF
  		}
  	}()
  	
  	req = new(Request)
  	req.datab = new(bytes.Buffer)
	//{json}
  	
	var ij RequestIOT
	err = json.NewDecoder( io.TeeReader(bufr, req.datab) ).Decode(&ij)
	if err != nil {
		return nil, verror.TrackErrorf("请求 json 内容格式不正确 %v", err)
	}
	if ij.Nonce == "" {
		return nil, verror.TrackErrorf("请求 nonce 序号不能为\"\"")
	}
	if !validMethod(ij.Method) {
		return nil, verror.TrackErrorf("请求无效的方法 %q", ij.Method)
	}
	
	var ok bool
	if req.ProtoMajor, req.ProtoMinor, ok = ParseIOTVersion(ij.Proto); !ok {
		return nil, verror.TrackErrorf("请求格式错误的IOT版本 %q", ij.Proto)
	}
	
	if req.URL, err = url.ParseRequestURI(ij.Path); err != nil {
		return nil, verror.TrackError(err)
  	}
  	
  	//释放内存，仅POST提交才支持body
  	if ij.Method != "POST" {
  		req.datab = nil
  	}
  	
	req.Header		= ij.Header.clone()
	for hk, hv := range req.Header {
		if !httpguts.ValidHeaderFieldName(hk) {
			return nil, verror.TrackErrorf("无效的标题名称 %s", hk)
		}
		if !httpguts.ValidHeaderFieldValue(hv) {
			return nil, verror.TrackErrorf("无效的标题值 %s", hv)
		}
	}
  	req.nonce		= ij.Nonce
	req.Method 		= ij.Method
	req.RequestURI	= ij.Path
	req.Proto		= ij.Proto
	req.Home		= ij.Home
	req.Close 		= shouldClose(req.ProtoMajor, req.ProtoMinor, req.Header)
	
	return req, nil
}

//解析响应
//	b io.Reader		需解析的数据，重要提醒：不要包含多个json块，它只能解析一个json块，其它数据块会被丢弃。这会清空你的io.Reader。
//	res *Response	响应
//	err error		错误
func ReadResponse(r *bufio.Reader, req *Request) (res *Response, err error){
	res, err = readResponse(r)
	if err != nil {
		return
	}
	res.Request = req
	res.RemoteAddr = req.RemoteAddr
	return
}
func readResponse(b io.Reader) (res *Response, err error) {
	bufr := newBufioReader(b)
	defer putBufioReader(bufr)
	
	res = new(Response)
	//{json}
	
	var riot ResponseIOT
	err = json.NewDecoder( b ).Decode(&riot)
	if err != nil {
		return nil, verror.TrackErrorf("响应 json 内容格式不正确 %v", err)
	}
	
	if riot.Nonce == "" {
		return nil, verror.TrackErrorf("响应 nonce 序号为\"\"")
	}
	res.Header	= riot.Header.clone()
	for hk, hv := range res.Header {
		if !httpguts.ValidHeaderFieldName(hk) {
			return nil, verror.TrackErrorf("无效的标题名称 %s", hk)
		}
		if !httpguts.ValidHeaderFieldValue(hv) {
			return nil, verror.TrackErrorf("无效的标题值 %s", hv)
		}
	}
	res.nonce 	= riot.Nonce
	res.Status	= riot.Status
	res.Body	= riot.Body
	res.Close	= shouldClose(1, 1, res.Header)
	
	return res, nil
}

//应该关闭，判断请求协议是否支持长连接
func shouldClose(major, minor int, header Header) bool {
	if major < 1 {
		return true
	}

	conv := header["Connection"]
	hasClose := conv == "close"
	if major == 1 && minor == 0 {
		return hasClose || conv != "keep-alive"
	}
	return hasClose
}

var (
  	bufioReaderPool   sync.Pool
  	bufioWriter2kPool sync.Pool
  	bufioWriter4kPool sync.Pool
)

//提取读取缓冲
func newBufioReader(r io.Reader) *bufio.Reader {
	if v := bufioReaderPool.Get(); v != nil {
		br := v.(*bufio.Reader)
		br.Reset(r)
		return br
	}
	return bufio.NewReader(r)
}

//回收读取缓冲
func putBufioReader(br *bufio.Reader) {
  	br.Reset(nil)
  	bufioReaderPool.Put(br)
}

//分配写入缓冲
func bufioWriterPool(size int) *sync.Pool {
  	switch size {
  	case 2 << 10:
  		return &bufioWriter2kPool
  	case 4 << 10:
  		return &bufioWriter4kPool
  	}
  	return nil
}

//回收写入缓冲
func putBufioWriter(bw *bufio.Writer) {
  	bw.Reset(nil)
  	if pool := bufioWriterPool(bw.Available()); pool != nil {
  		pool.Put(bw)
  	}
}

//提取写入缓冲
func newBufioWriterSize(w io.Writer, size int) *bufio.Writer {
  	pool := bufioWriterPool(size)
  	if pool != nil {
  		if v := pool.Get(); v != nil {
  			bw := v.(*bufio.Writer)
  			bw.Reset(w)
  			return bw
  		}
  	}
  	return bufio.NewWriterSize(w, size)
}

//判断状态码
func bodyAllowedForStatus(status int) bool {
	switch {
	case status >= 100 && status <= 199:
		return false
	case status == 204:
		return false
	case status == 304:
		return false
	}
	return true
}

//判断toKen
func hasToken(v, token string) bool {
  	if len(token) > len(v) || token == "" {
  		return false
  	}
  	if v == token {
  		return true
  	}
  	for sp := 0; sp <= len(v)-len(token); sp++ {
  		// Check that first character is good.
  		// The token is ASCII, so checking only a single byte
  		// is sufficient. We skip this potential starting
  		// position if both the first byte and its potential
  		// ASCII uppercase equivalent (b|0x20) don't match.
  		// False positives ('^' => '~') are caught by EqualFold.
  		if b := v[sp]; b != token[0] && b|0x20 != token[0] {
  			continue
  		}
  		// Check that start pos is on a valid token boundary.
  		if sp > 0 && !isTokenBoundary(v[sp-1]) {
  			continue
  		}
  		// Check that end pos is on a valid token boundary.
  		if endPos := sp + len(token); endPos != len(v) && !isTokenBoundary(v[endPos]) {
  			continue
  		}
  		if strings.EqualFold(v[sp:sp+len(token)], token) {
  			return true
  		}
  	}
  	return false
}

//是无效符号
func isTokenBoundary(b byte) bool {
  	return b == ' ' || b == ',' || b == '\t'
}

//解析基本验证
func parseBasicAuth(auth string) (username, password string, ok bool) {
  	const prefix = "Basic "
  	if !strings.HasPrefix(auth, prefix) {
  		return
  	}
  	c, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
  	if err != nil {
  		return
  	}
  	cs := string(c)
  	s := strings.IndexByte(cs, ':')
  	if s < 0 {
  		return
  	}
  	return cs[:s], cs[s+1:], true
}

//设置基本验证
func basicAuth(username, password string) string {
	auth := username + ":" + password
	return base64.StdEncoding.EncodeToString([]byte(auth))
}


//生成编号
//	nonce string	编号
//	err error		错误
func Nonce() (nonce string, err error) {
	//创建编号
	bigInt, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt32))
	if err != nil {
		return "", verror.TrackErrorf("创建 nonce 编号失败 %v", err)
	}
	//提取编号
	d, err := bigInt.MarshalText()
	if err != nil {
		return "", verror.TrackErrorf("提取 nonce 编号失败 %v", err)
	}
	return string(d), nil
}

//是网络读取失败
func isCommonNetReadError(err error) bool {
	if neterr, ok := err.(net.Error); ok && neterr.Timeout() {
		//网络失败,不要回复
		return true
	}
	if oe, ok := err.(*net.OpError); ok && oe.Op == "read" {
		//网络失败,不要回复
		return true
	}

	// 读取错误或者被劫持连接
	if err == io.EOF || err == io.ErrUnexpectedEOF || err == ErrHijacked {
		//读取失败，不要回复
		return true
	}
	return false
}

//快速设置错误
//	w ResponseWriter	响应
//	err string			错误字符串
//	code int			错误代码
func Error(w ResponseWriter, err string, code int){
	w.Status(code)
	w.Header().Set("Connection","close")
	w.SetBody(err)
}




